package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ToolSpec struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Risk        RiskLevel `json:"risk"`
	Parameters  map[string]any
}

type ToolRequest struct {
	WorkspaceScope string
	Arguments      map[string]any
}

type ToolResult struct {
	JSON string
}

type Tool interface {
	Spec() ToolSpec
	Execute(context.Context, ToolRequest) (ToolResult, error)
}

type ToolRegistry struct {
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

func (r *ToolRegistry) Register(tool Tool) {
	if r == nil || tool == nil {
		return
	}
	r.tools[tool.Spec().Name] = tool
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) Specs() []ToolSpec {
	if r == nil {
		return nil
	}
	specs := make([]ToolSpec, 0, len(r.tools))
	for _, tool := range r.tools {
		specs = append(specs, tool.Spec())
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

func RegisterFilesystemTools(registry *ToolRegistry) {
	registry.Register(filesystemListDirTool{})
	registry.Register(filesystemReadFileTool{})
}

type filesystemListDirTool struct{}

func (filesystemListDirTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "filesystem.list_dir",
		Description: "Use when you need to inspect available local files or directories before answering. Arguments: path, a relative directory path under workspace_scope. Do not use for web/current information.",
		Risk:        RiskLow,
		Parameters: objectSchema(map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative directory path under workspace_scope. Use . for the workspace root.",
			},
		}, []string{"path"}),
	}
}

func (filesystemListDirTool) Execute(ctx context.Context, req ToolRequest) (ToolResult, error) {
	target, displayPath, err := resolveWorkspacePath(req.WorkspaceScope, stringArg(req.Arguments, "path"))
	if err != nil {
		return ToolResult{}, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return ToolResult{}, err
	}
	payload := struct {
		Path    string `json:"path"`
		Entries []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entries"`
	}{Path: displayPath}
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return ToolResult{}, ctx.Err()
		default:
		}
		entryType := "file"
		if entry.IsDir() {
			entryType = "directory"
		}
		payload.Entries = append(payload.Entries, struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}{Name: entry.Name(), Type: entryType})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{JSON: string(data)}, nil
}

type filesystemReadFileTool struct{}

func (filesystemReadFileTool) Spec() ToolSpec {
	return ToolSpec{
		Name:        "filesystem.read_file",
		Description: "Use when the answer depends on the contents of a known local file. Arguments: path, a relative file path under workspace_scope. Do not guess file contents; call this tool first.",
		Risk:        RiskLow,
		Parameters: objectSchema(map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative file path under workspace_scope.",
			},
		}, []string{"path"}),
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func (filesystemReadFileTool) Execute(ctx context.Context, req ToolRequest) (ToolResult, error) {
	target, displayPath, err := resolveWorkspacePath(req.WorkspaceScope, stringArg(req.Arguments, "path"))
	if err != nil {
		return ToolResult{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return ToolResult{}, err
	}
	if info.IsDir() {
		return ToolResult{}, fmt.Errorf("%s is a directory", displayPath)
	}
	if info.Size() > 1<<20 {
		return ToolResult{}, fmt.Errorf("%s is larger than 1MiB", displayPath)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return ToolResult{}, err
	}
	select {
	case <-ctx.Done():
		return ToolResult{}, ctx.Err()
	default:
	}
	payload := struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{Path: displayPath, Content: string(data)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{JSON: string(encoded)}, nil
}

func resolveWorkspacePath(scope, requested string) (string, string, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "."
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "."
	}
	if filepath.IsAbs(requested) {
		return "", "", errors.New("absolute paths are not allowed")
	}
	root, err := filepath.Abs(scope)
	if err != nil {
		return "", "", err
	}
	target, err := filepath.Abs(filepath.Join(root, requested))
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("path escapes workspace scope")
	}
	if relative == "." {
		return target, ".", nil
	}
	return target, filepath.ToSlash(relative), nil
}

func stringArg(args map[string]any, name string) string {
	if args == nil {
		return ""
	}
	value, ok := args[name]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
