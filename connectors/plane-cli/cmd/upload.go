package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/client"
	"github.com/trebi-ai/trebi-connectors/connectors/plane-cli/internal/spec"
)

// uploader runs the three-step file upload: create the record (Plane returns a
// presigned storage form), post the file to storage, then confirm the upload.
type uploader struct {
	create  spec.Endpoint
	confirm spec.Endpoint
	usage   string
	flags   []cli.Flag
	body    func(c *cli.Command, body map[string]any)
}

func newUploader(s *spec.Spec, create, confirm string) uploader {
	return uploader{create: findEndpoint(s, create), confirm: findEndpoint(s, confirm)}
}

func findEndpoint(s *spec.Spec, path string) spec.Endpoint {
	for _, c := range s.Commands {
		if strings.Join(c.Path, " ") == path {
			return c.Variants[0]
		}
	}
	panic("spec has no command " + path)
}

// Command builds the `upload` leaf.
func (u uploader) Command() *cli.Command {
	parents := u.argsUsage()
	return &cli.Command{
		Name:      "upload",
		Usage:     u.usage,
		ArgsUsage: strings.TrimSpace(parents + " <file>"),
		Description: "Creates the record, uploads the file to the presigned storage URL that Plane returns,\n" +
			"then confirms the upload. Prints the confirmed record.",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "name", Usage: "Display name (default: the file name)"},
			&cli.StringFlag{Name: "content-type", Usage: "MIME type (default: from the file extension)"},
		}, u.flags...),
		Action: u.run,
	}
}

func (u uploader) argsUsage() string {
	var parts []string
	for _, n := range positional(u.create) {
		parts = append(parts, "<"+argName(n)+">")
	}
	return strings.Join(parts, " ")
}

func (u uploader) run(ctx context.Context, c *cli.Command) error {
	a := FromCmd(c)
	args := c.Args().Slice()
	if len(args) != len(positional(u.create))+1 {
		return fmt.Errorf("expected arguments: %s", strings.TrimSpace(u.argsUsage()+" <file>"))
	}
	parents, file := args[:len(args)-1], args[len(args)-1]
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	name := c.String("name")
	if name == "" {
		name = filepath.Base(file)
	}
	ctype := c.String("content-type")
	if ctype == "" {
		// Plane rejects parameters such as "; charset=utf-8".
		ctype, _, _ = strings.Cut(mime.TypeByExtension(filepath.Ext(file)), ";")
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	body := map[string]any{"name": name, "size": st.Size(), "type": ctype}
	if u.body != nil {
		u.body(c, body)
	}

	cl, err := a.Client()
	if err != nil {
		return err
	}
	path, err := resolvePath(ctx, a, cl, u.create, parents)
	if err != nil {
		return err
	}
	resp, err := cl.Do(ctx, client.Request{Method: u.create.Method, Path: path, Body: body})
	if err != nil {
		return err
	}
	target, ok := client.FindUploadTarget(resp.Body)
	if !ok {
		return fmt.Errorf("create response has no upload_data; response: %s", resp.Body)
	}
	id := recordID(resp.Body)
	if id == "" {
		return fmt.Errorf("create response has no id; response: %s", resp.Body)
	}
	if err := cl.Upload(ctx, target, name, f); err != nil {
		return err
	}
	path, err = resolvePath(ctx, a, cl, u.confirm, append(parents, id))
	if err != nil {
		return err
	}
	resp, err = cl.Do(ctx, client.Request{Method: http.MethodPatch, Path: path, Body: map[string]any{"is_uploaded": true}})
	if err != nil {
		return fmt.Errorf("file uploaded but confirm failed (id %s): %w", id, err)
	}
	successf(a, os.Stderr, "Uploaded %s (%d bytes) as %s", name, st.Size(), id)
	return a.Render(c.Root().Writer, resp.Body)
}

// recordID reads the record id from a create response. Plane names it "id",
// "asset_id", or nests it under "asset"/"attachment".
func recordID(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	for _, k := range []string{"id", "asset_id"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	for _, k := range []string{"asset", "attachment"} {
		if sub, ok := m[k].(map[string]any); ok {
			if s, ok := sub["id"].(string); ok {
				return s
			}
		}
	}
	return ""
}

// UploadCommands returns the upload and download helpers keyed by noun path.
func UploadCommands(s *spec.Spec) map[string][]*cli.Command {
	attachment := newUploader(s, "work-item attachment create", "work-item attachment confirm")
	attachment.usage = "Upload a file as a work item attachment"

	asset := newUploader(s, "asset create", "asset confirm")
	asset.usage = "Upload a file as a workspace asset"
	asset.flags = []cli.Flag{&cli.StringFlag{Name: "entity-type", Usage: "Asset entity type"}}
	asset.body = setEntityType

	userAsset := newUploader(s, "user asset create", "user asset confirm")
	userAsset.usage = "Upload an avatar or cover image"
	userAsset.flags = []cli.Flag{&cli.StringFlag{Name: "entity-type", Value: "USER_AVATAR", Usage: "USER_AVATAR or USER_COVER"}}
	userAsset.body = setEntityType

	attachmentDL := newDownloader(s, "attachment", "work-item attachment", true)
	attachmentDL.usage = "Download work item attachments"

	// The asset list holds every attachment of the workspace, so ids are required.
	assetDL := newDownloader(s, "asset", "asset", false)
	assetDL.usage = "Download workspace assets"

	return map[string][]*cli.Command{
		"work-item attachment": {attachment.Command(), attachmentDL.Command()},
		"asset":                {asset.Command(), assetDL.Command()},
		"user asset":           {userAsset.Command()},
	}
}

func setEntityType(c *cli.Command, body map[string]any) {
	if v := c.String("entity-type"); v != "" {
		body["entity_type"] = v
	}
}
