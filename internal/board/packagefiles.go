package board

import (
	"context"
	"path"
	"slices"
	"strings"

	"github.com/kesokaj/crew/internal/gitstore"
)

// Package files are the files whose change can add, remove, or move a package that the project's code
// pulls in, or change where packages come from (13.8, since 0.90): a manifest or a package source's
// settings, whose diff the reviewer is shown, and a lock file, which it is shown by name and reads in
// its workspace. A ticket whose branch changes one is reviewed on its head before it merges, also when
// Analysis skipped Review (7.4).

var (
	manifestNames = []string{"go.mod", "go.work", "package.json", "constraints.txt", "pyproject.toml", "setup.py",
		"setup.cfg", "Pipfile", "Cargo.toml", "Gemfile", "composer.json", "pom.xml", "build.gradle", "build.gradle.kts",
		"settings.gradle", "settings.gradle.kts", "libs.versions.toml", "deno.json", "deno.jsonc", "Package.swift", "mix.exs",
		"packages.config", "Directory.Packages.props", "Directory.Build.props", "gems.rb", "pnpm-workspace.yaml", "package.yaml",
		"package.json5",
		// Where packages, and the package managers themselves, come from.
		".npmrc", ".yarnrc", ".yarnrc.yml", ".pnpmfile.cjs", "pip.conf", "uv.toml", "bunfig.toml", "nuget.config", "NuGet.config",
		"NuGet.Config", "gradle-wrapper.properties", "maven-wrapper.properties"}
	manifestSuffixes = []string{".csproj", ".fsproj", ".vbproj", ".gemspec"}
	lockNames        = []string{"go.sum", "go.work.sum", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock",
		"bun.lockb", "Pipfile.lock", "poetry.lock", "uv.lock", "Cargo.lock", "Gemfile.lock", "composer.lock", "deno.lock",
		"Package.resolved", "mix.lock", "gradle.lockfile", "packages.lock.json", "gems.locked", "pdm.lock",
		// A package manager kept in the repository, which its wrapper script runs: shown by name.
		"gradle-wrapper.jar", "maven-wrapper.jar"}
)

// PackageFileKind says whether a path is a package manifest or a package source's settings
// ("manifest"), a lock file ("lock"), or neither (""), by its base name at any depth; pip's
// requirements and their pip-tools inputs also as any .txt or .in in a folder requirements, Cargo's
// and Bundler's settings by their folders .cargo and .bundle, Maven's by .mvn, and a package manager
// kept in the repository, a wrapper's jar or under .yarn/releases or .yarn/plugins, as a lock file:
// shown by name.
func PackageFileKind(p string) string {
	base, dir := path.Base(p), path.Base(path.Dir(p))
	switch {
	case slices.Contains(lockNames, base),
		strings.HasPrefix(p, ".yarn/releases/") || strings.HasPrefix(p, ".yarn/plugins/") ||
			strings.Contains(p, "/.yarn/releases/") || strings.Contains(p, "/.yarn/plugins/"):
		return "lock"
	case slices.Contains(manifestNames, base),
		(strings.HasSuffix(base, ".txt") || strings.HasSuffix(base, ".in")) && (strings.Contains(base, "requirements") || dir == "requirements"),
		slices.ContainsFunc(manifestSuffixes, func(s string) bool { return strings.HasSuffix(base, s) && base != s }),
		dir == ".cargo" && (base == "config.toml" || base == "config"),
		dir == ".bundle" && base == "config",
		dir == ".mvn" && (base == "maven.config" || base == "extensions.xml" || base == "settings.xml"):
		return "manifest"
	}
	return ""
}

// PackageFile is a package file a ticket's branch adds, changes, or deletes, with its diff against main;
// a lock file's diff is left out.
type PackageFile struct {
	gitstore.FileDiff
	Lock    bool
	Deleted bool
}

// PackageFiles are the package files the ticket's branch at head changes or deletes, against where it
// left main: what its reviewer is shown. Which files they are comes from git's full list, as for
// TestFiles; only a manifest's diff may be cut, and says so.
func (b *Board) PackageFiles(ctx context.Context, p *Project, head string) ([]PackageFile, error) {
	return packageFiles(ctx, b.Git, p, head)
}

func packageFiles(ctx context.Context, g *gitstore.Store, p *Project, head string) ([]PackageFile, error) {
	touched, err := g.Touched(ctx, p.Name, "main", head)
	if err != nil {
		return nil, err
	}
	var paths []string
	for path := range touched {
		if PackageFileKind(path) != "" {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	diffs, err := g.Compare(ctx, p.Name, "main", head)
	if err != nil {
		return nil, err
	}
	byPath := map[string]gitstore.FileDiff{}
	for _, d := range diffs {
		byPath[d.Path] = d
	}
	out := make([]PackageFile, 0, len(paths))
	for _, path := range paths {
		lock := PackageFileKind(path) == "lock"
		d, ok := byPath[path]
		switch {
		case !ok:
			// Past the cap of what Compare lists: its counts are not known either.
			d = gitstore.FileDiff{Path: path, Cut: true}
		case lock:
			d.Patch, d.Cut = "", false
		}
		out = append(out, PackageFile{FileDiff: d, Lock: lock, Deleted: touched[path]})
	}
	slices.SortFunc(out, func(a, b PackageFile) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}
