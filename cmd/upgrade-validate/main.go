package main
import("fmt";"os";"git.host.bzh/pepe/kuberpack/internal/spec")
func main(){b,e:=os.ReadFile(os.Args[1]);if e!=nil{panic(e)};m,e:=spec.Parse(b);if e!=nil{panic(e)};fmt.Printf("valid manifest: project=%s namespace=%s services=%d\n",m.Project,m.Namespace,len(m.Services))}
