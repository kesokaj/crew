package assistant

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/gitstore"
)

const maxPreviewFile = 4 << 20

// FolderFile is one file in a conversation's saved work folder.
type FolderFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	Previewable bool   `json:"previewable"`
}

type folderOpener interface {
	OpenFolder(ctx context.Context, conv string) ([]byte, error)
}

func (s *Service) openFolderTar(ctx context.Context, id string) ([]byte, error) {
	if _, err := s.conv(ctx, id); err != nil {
		return nil, err
	}
	fo, ok := s.Runs.(folderOpener)
	if !ok || fo == nil {
		return nil, nil
	}
	return fo.OpenFolder(ctx, id)
}

func cleanTarPath(name string) (string, bool) {
	p := path.Clean(strings.TrimPrefix(strings.TrimPrefix(name, "./"), "/"))
	if p == "" || p == "." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.EqualFold(seg, ".git") {
			return "", false
		}
	}
	return p, true
}

func isPreviewable(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	switch ext {
	case ".html", ".htm", ".svg", ".png", ".jpg", ".jpeg", ".webp", ".css", ".md", ".txt", ".json", ".yaml", ".yml", ".ts", ".tsx", ".js", ".go":
		return true
	}
	return false
}

func previewContentType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".css":
		return "text/css; charset=utf-8"
	case ".md", ".txt", ".json", ".yaml", ".yml", ".ts", ".tsx", ".js", ".go":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// Files lists the files saved in a conversation's work folder.
func (s *Service) Files(ctx context.Context, id string) ([]FolderFile, error) {
	gz, err := s.openFolderTar(ctx, id)
	if err != nil || len(gz) == 0 {
		return []FolderFile{}, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	out := []FolderFile{}
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hd.Typeflag != tar.TypeReg {
			continue
		}
		p, ok := cleanTarPath(hd.Name)
		if !ok {
			continue
		}
		out = append(out, FolderFile{Path: p, Size: hd.Size, Previewable: isPreviewable(p)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// File reads one file from a conversation's saved work folder for sandboxed preview.
func (s *Service) File(ctx context.Context, id, want string) ([]byte, string, error) {
	want, ok := cleanTarPath(want)
	if !ok {
		return nil, "", invalid("path: a file in the work folder")
	}
	gz, err := s.openFolderTar(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if len(gz) == 0 {
		return nil, "", ErrNotFound
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, "", err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", err
		}
		if hd.Typeflag != tar.TypeReg {
			continue
		}
		p, ok := cleanTarPath(hd.Name)
		if !ok || p != want {
			continue
		}
		if hd.Size > maxPreviewFile {
			return nil, "", refused("the file is larger than %d MB", maxPreviewFile>>20)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxPreviewFile))
		if err != nil {
			return nil, "", err
		}
		return data, previewContentType(p), nil
	}
	return nil, "", ErrNotFound
}

// PushBranch commits the files of a conversation's saved work folder onto branch conv/<short-id> in
// the target project's repository so a builder or the operator can inspect or merge them.
func (s *Service) PushBranch(ctx context.Context, id, projectRef string) (string, string, string, error) {
	c, err := s.conv(ctx, id)
	if err != nil {
		return "", "", "", err
	}
	proj, err := s.targetProject(ctx, c, projectRef)
	if err != nil {
		return "", "", "", err
	}
	if s.Board == nil || s.Board.Git == nil {
		return "", "", "", refused("git is not available")
	}
	if s.Board.Identity == nil {
		return "", "", "", refused("no git identity: set it up in Setup first")
	}
	ident, err := s.Board.Identity(ctx)
	if err != nil {
		return "", "", "", refused("no git identity: set it up in Setup first (%v)", err)
	}
	gz, err := s.openFolderTar(ctx, id)
	if err != nil {
		return "", "", "", err
	}
	if len(gz) == 0 {
		return "", "", "", refused("the conversation has no saved work folder")
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", "", "", err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	overlay := gitstore.Overlay{}
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", "", err
		}
		if hd.Typeflag != tar.TypeReg || hd.Size > maxPreviewFile {
			continue
		}
		p, ok := cleanTarPath(hd.Name)
		if !ok || cli.Matches(p, cli.InstructionFiles()) || cli.Matches(p, cli.CodeFiles()) {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxPreviewFile))
		if err != nil {
			return "", "", "", err
		}
		overlay[p] = func([]byte) []byte { return data }
	}
	if len(overlay) == 0 {
		return "", "", "", refused("the work folder has no files to push")
	}
	branch := "conv/" + id[:8]
	msg := fmt.Sprintf("Work folder from %s\n\nConversation: %s\n", c.where(), id)
	commit, err := s.Board.Git.CommitBranchOverlay(ctx, proj.Name, branch, overlay, msg, ident)
	if err != nil {
		return "", "", "", err
	}
	return proj.Name, branch, commit, nil
}
