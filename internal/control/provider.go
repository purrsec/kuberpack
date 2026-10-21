package control

import (
	"git.host.bzh/pepe/kuberpack/internal/git"
	"git.host.bzh/pepe/kuberpack/internal/store"
)

// appForge finds who serves the repository of an application.
//
// A repository written "owner/name" belongs to the forge of the platform.
// A repository written as its URL lives on the forge of that URL: a third
// party application is built from a repository Kuberpack cannot write in.
func (s *Server) appForge(app store.App) (git.Provider, string, string, error) {
	return git.Resolve(app.ForgejoRepository, s.cfg.Provider, s.cfg.Token)
}
