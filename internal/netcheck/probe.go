package netcheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	// DefaultImage doit être tirable par le cluster cible. busybox fournit sh,
	// wget et httpd, sans dépendance à un registre propre à Kuberpack.
	DefaultImage = "docker.io/library/busybox:1.37.0"
	// ProbePort est le port TCP du pod cible.
	ProbePort       = 8080
	probeLabel      = "kuberpack.io/netcheck"
	targetWait      = 90 * time.Second
	jobWait         = 90 * time.Second
	cleanupTimeout  = 60 * time.Second
	denyAllAttempts = 3
)

// Outcome décrit le résultat d'un client réseau.
type Outcome string

const (
	OutcomeReachable    Outcome = "joignable"
	OutcomeBlocked      Outcome = "bloqué"
	OutcomeInconclusive Outcome = "indéterminé"
)

// Verdict est la conclusion de la probe. Il ne prouve que le scénario testé,
// pas l'ensemble des flux du cluster.
type Verdict string

const (
	VerdictEnforced     Verdict = "appliquée"
	VerdictNotEnforced  Verdict = "non appliquée"
	VerdictInconclusive Verdict = "indéterminée"
)

// ClientRunner exécute un Job client et renvoie ce qu'il a observé. Il est
// injectable pour tester l'orchestration sans cluster réel.
type ClientRunner interface {
	Run(ctx context.Context, namespace string, job *batchv1.Job) (Outcome, error)
}

// ProbeOptions décrit une probe. Namespace vide génère un nom temporaire.
type ProbeOptions struct {
	Namespace string
	Image     string
	Timeout   time.Duration
	Runner    ClientRunner
}

// ProbeReport contient les preuves et l'état du nettoyage.
type ProbeReport struct {
	Namespace     string
	Positive      Outcome
	Negative      Outcome
	Verdict       Verdict
	Cleanup       string
	CleanupFailed bool
	Notes         []string
}

// RunProbe crée un namespace temporaire, une cible HTTP et un client, vérifie
// le trafic avant policy (contrôle positif), applique un deny-all, vérifie le
// refus, puis supprime le namespace. Aucun Secret n'est lu ou écrit.
func RunProbe(ctx context.Context, client kubernetes.Interface, opts ProbeOptions) (report ProbeReport, err error) {
	if opts.Image == "" {
		opts.Image = DefaultImage
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Minute
	}
	if opts.Runner == nil {
		opts.Runner = &jobRunner{client: client}
	}
	report = ProbeReport{
		Positive: OutcomeInconclusive,
		Negative: OutcomeInconclusive,
		Verdict:  VerdictInconclusive,
	}

	namespace := strings.TrimSpace(opts.Namespace)
	if namespace == "" {
		namespace, err = randomNamespace()
		if err != nil {
			return report, err
		}
	}
	report.Namespace = namespace

	namespaceObject := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":          "kuberpack",
				probeLabel:                           "true",
				"pod-security.kubernetes.io/enforce": "baseline",
				"pod-security.kubernetes.io/warn":    "restricted",
			},
		},
	}
	if _, err = client.CoreV1().Namespaces().Create(ctx, namespaceObject, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return report, fmt.Errorf("le namespace temporaire %q existe déjà : choisir un autre nom", namespace)
		}
		return report, fmt.Errorf("création du namespace temporaire %q: %w", namespace, err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if cleanupErr := deleteNamespace(cleanupCtx, client, namespace); cleanupErr != nil {
			report.Cleanup = "échec: " + cleanupErr.Error()
			report.CleanupFailed = true
			return
		}
		report.Cleanup = "namespace supprimé"
	}()

	probeCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	address, err := createTarget(probeCtx, client, namespace, opts.Image)
	if err != nil {
		report.Notes = append(report.Notes, "le pod cible n'est pas prêt : "+err.Error())
		return report, nil
	}
	targetURL := "http://" + net.JoinHostPort(address, strconv.Itoa(ProbePort)) + "/"
	report.Notes = append(report.Notes, "cible HTTP : "+targetURL)

	report.Positive, err = runClient(probeCtx, opts.Runner, namespace, "netcheck-client-allow", opts.Image, targetURL)
	if err != nil {
		report.Notes = append(report.Notes, "contrôle positif impossible : "+err.Error())
		return report, nil
	}
	if report.Positive != OutcomeReachable {
		report.Notes = append(report.Notes, "contrôle positif négatif : le client ne joint pas la cible avant toute policy (image, CNI ou réseau du banc). Un refus ensuite serait non concluant.")
		return report, nil
	}

	if err := applyDenyAll(probeCtx, client, namespace); err != nil {
		return report, fmt.Errorf("création de la NetworkPolicy deny-all: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= denyAllAttempts; attempt++ {
		name := fmt.Sprintf("netcheck-client-deny-%d", attempt)
		outcome, runErr := runClient(probeCtx, opts.Runner, namespace, name, opts.Image, targetURL)
		if runErr != nil {
			lastErr = runErr
			report.Notes = append(report.Notes, fmt.Sprintf("tentative deny-all %d : %v", attempt, runErr))
			continue
		}
		report.Negative = outcome
		if outcome == OutcomeBlocked {
			break
		}
		if attempt < denyAllAttempts {
			if sleepErr := sleepContext(probeCtx, 2*time.Second); sleepErr != nil {
				lastErr = sleepErr
				break
			}
		}
	}

	switch report.Negative {
	case OutcomeBlocked:
		report.Verdict = VerdictEnforced
	case OutcomeReachable:
		report.Verdict = VerdictNotEnforced
		report.Notes = append(report.Notes, "la cible reste joignable après le deny-all : le CNI n'applique pas cette policy (moteur inconnu, agent désactivé, mode non strict ou propagation en échec).")
	default:
		report.Verdict = VerdictInconclusive
		if lastErr != nil {
			report.Notes = append(report.Notes, "dernière erreur deny-all : "+lastErr.Error())
		}
	}
	return report, nil
}

func runClient(ctx context.Context, runner ClientRunner, namespace, name, image, targetURL string) (Outcome, error) {
	return runner.Run(ctx, namespace, clientJob(namespace, name, image, targetURL))
}

func targetDeployment(namespace, image string) *appsv1.Deployment {
	labels := map[string]string{
		probeLabel:                  "target",
		"app.kubernetes.io/name":    "kuberpack-netcheck-target",
		"app.kubernetes.io/part-of": "kuberpack",
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "netcheck-target", Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Pointer(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{probeLabel: "target"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: boolPointer(false),
					RestartPolicy:                corev1.RestartPolicyAlways,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   boolPointer(true),
						RunAsUser:      int64Pointer(65534),
						RunAsGroup:     int64Pointer(65534),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: []corev1.Volume{{
						Name:         "www",
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					}},
					Containers: []corev1.Container{{
						Name:            "target",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"sh", "-c"},
						Args:            []string{fmt.Sprintf("mkdir -p /tmp/www && printf ok > /tmp/www/index.html && exec httpd -f -p %d -h /tmp/www", ProbePort)},
						Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: ProbePort, Protocol: corev1.ProtocolTCP}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "www", MountPath: "/tmp/www"}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: boolPointer(false),
							ReadOnlyRootFilesystem:   boolPointer(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
}

func clientJob(namespace, name, image, targetURL string) *batchv1.Job {
	labels := map[string]string{
		probeLabel:                  "client",
		"app.kubernetes.io/name":    "kuberpack-netcheck-client",
		"app.kubernetes.io/part-of": "kuberpack",
	}
	script := `if wget -q -T 3 -O /dev/null "$TARGET_URL"; then echo reachable; exit 1; else echo blocked; exit 0; fi`
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:          int32Pointer(0),
			ActiveDeadlineSeconds: int64Pointer(60),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: boolPointer(false),
					RestartPolicy:                corev1.RestartPolicyNever,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   boolPointer(true),
						RunAsUser:      int64Pointer(65534),
						RunAsGroup:     int64Pointer(65534),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            "client",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"sh", "-c"},
						Args:            []string{script},
						Env:             []corev1.EnvVar{{Name: "TARGET_URL", Value: targetURL}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: boolPointer(false),
							ReadOnlyRootFilesystem:   boolPointer(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
}

func createTarget(ctx context.Context, client kubernetes.Interface, namespace, image string) (string, error) {
	if _, err := client.AppsV1().Deployments(namespace).Create(ctx, targetDeployment(namespace, image), metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("création du Deployment cible: %w", err)
	}
	var address string
	err := wait.PollUntilContextTimeout(ctx, time.Second, targetWait, true, func(ctx context.Context) (bool, error) {
		pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: probeLabel + "=target"})
		if err != nil {
			return false, err
		}
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil || pod.Status.PodIP == "" {
				continue
			}
			if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == "" {
				address = pod.Status.PodIP
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("attente d'une IP pour le pod cible: %w", err)
	}
	return address, nil
}

func applyDenyAll(ctx context.Context, client kubernetes.Interface, namespace string) error {
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "netcheck-deny-all", Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		},
	}
	_, err := client.NetworkingV1().NetworkPolicies(namespace).Create(ctx, policy, metav1.CreateOptions{})
	return err
}

func deleteNamespace(ctx context.Context, client kubernetes.Interface, namespace string) error {
	err := client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 45*time.Second, true, func(ctx context.Context) (bool, error) {
		_, getErr := client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			return true, nil
		}
		return false, getErr
	})
}

func randomNamespace() (string, error) {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("nom de namespace temporaire: %w", err)
	}
	return "kuberpack-netcheck-" + hex.EncodeToString(raw), nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type jobRunner struct {
	client kubernetes.Interface
}

// Run crée le Job, attend sa fin puis lit ses logs. Le code de sortie du Job
// n'est pas suffisant : une erreur d'image ou de démarrage échoue aussi. Le
// marqueur écrit par le script distingue une vraie observation réseau.
func (r *jobRunner) Run(ctx context.Context, namespace string, job *batchv1.Job) (Outcome, error) {
	created, err := r.client.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return OutcomeInconclusive, fmt.Errorf("création du Job %q: %w", job.Name, err)
	}
	name := created.Name
	err = wait.PollUntilContextTimeout(ctx, time.Second, jobWait, true, func(ctx context.Context) (bool, error) {
		current, err := r.client.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return OutcomeInconclusive, fmt.Errorf("attente du Job %q: %w", name, err)
	}
	logs, err := r.logs(ctx, namespace, name)
	if err != nil {
		return OutcomeInconclusive, err
	}
	switch {
	case strings.Contains(logs, "reachable"):
		return OutcomeReachable, nil
	case strings.Contains(logs, "blocked"):
		return OutcomeBlocked, nil
	default:
		return OutcomeInconclusive, fmt.Errorf("sortie du Job %q sans marqueur reachable/blocked: %.200q", name, logs)
	}
}

func (r *jobRunner) logs(ctx context.Context, namespace, jobName string) (string, error) {
	pods, err := r.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName})
	if err != nil {
		return "", fmt.Errorf("pods du Job %q illisibles: %w", jobName, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("aucun pod pour le Job %q", jobName)
	}
	stream, err := r.client.CoreV1().Pods(namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{Container: "client"}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("logs du Job %q illisibles: %w", jobName, err)
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, 1<<20))
	if err != nil {
		return "", fmt.Errorf("lecture des logs du Job %q: %w", jobName, err)
	}
	return string(data), nil
}

func boolPointer(value bool) *bool    { return &value }
func int32Pointer(value int32) *int32 { return &value }
func int64Pointer(value int64) *int64 { return &value }
