package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemToolsListAndReadWithinWorkspace(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello agent"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	registry := NewToolRegistry()
	RegisterFilesystemTools(registry)

	list, ok := registry.Get("filesystem.list_dir")
	if !ok {
		t.Fatal("filesystem.list_dir not registered")
	}
	listResult, err := list.Execute(ctx, ToolRequest{
		WorkspaceScope: workspace,
		Arguments:      map[string]any{"path": "."},
	})
	if err != nil {
		t.Fatalf("list Execute: %v", err)
	}
	var listed struct {
		Entries []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(listResult.JSON), &listed); err != nil {
		t.Fatalf("parse list result: %v", err)
	}
	if len(listed.Entries) != 2 {
		t.Fatalf("entries=%v, want notes.txt and docs", listed.Entries)
	}

	read, ok := registry.Get("filesystem.read_file")
	if !ok {
		t.Fatal("filesystem.read_file not registered")
	}
	readResult, err := read.Execute(ctx, ToolRequest{
		WorkspaceScope: workspace,
		Arguments:      map[string]any{"path": "notes.txt"},
	})
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	var readPayload struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(readResult.JSON), &readPayload); err != nil {
		t.Fatalf("parse read result: %v", err)
	}
	if readPayload.Content != "hello agent" {
		t.Fatalf("content=%q", readPayload.Content)
	}
}

func TestFilesystemToolsRejectPathOutsideWorkspace(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	registry := NewToolRegistry()
	RegisterFilesystemTools(registry)
	read, ok := registry.Get("filesystem.read_file")
	if !ok {
		t.Fatal("filesystem.read_file not registered")
	}

	_, err := read.Execute(ctx, ToolRequest{
		WorkspaceScope: workspace,
		Arguments:      map[string]any{"path": "../outside.txt"},
	})
	if err == nil {
		t.Fatal("read outside workspace returned nil error")
	}
}
