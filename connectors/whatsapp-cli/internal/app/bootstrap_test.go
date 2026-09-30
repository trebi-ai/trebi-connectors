package app

import (
	"context"
	"github.com/trebi-ai/trebi-connectors/connectors/whatsapp-cli/internal/wa/fakewa"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestRefreshContactsStoresContacts(t *testing.T) {
	a := newTestApp(t)
	f := fakewa.New()
	a.wa = f

	jid := types.JID{User: "111", Server: types.DefaultUserServer}
	f.AddContact(jid, types.ContactInfo{
		Found:     true,
		PushName:  "Push",
		FullName:  "Full Name",
		FirstName: "First",
	})

	if err := a.refreshContacts(context.Background()); err != nil {
		t.Fatalf("refreshContacts: %v", err)
	}
	c, err := a.db.GetContact(jid.String())
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.Name == "" {
		t.Fatalf("expected stored contact name, got empty")
	}
}

func TestRefreshGroupsStoresGroupsAndChats(t *testing.T) {
	a := newTestApp(t)
	f := fakewa.New()
	a.wa = f

	gid := types.JID{User: "12345", Server: types.GroupServer}
	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	f.AddGroup(&types.GroupInfo{
		JID:          gid,
		OwnerJID:     types.JID{User: "999", Server: types.DefaultUserServer},
		GroupName:    types.GroupName{Name: "MyGroup"},
		GroupCreated: created,
	})

	if err := a.refreshGroups(context.Background()); err != nil {
		t.Fatalf("refreshGroups: %v", err)
	}
	gs, err := a.db.ListGroups("MyGroup", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(gs) != 1 || gs[0].JID != gid.String() {
		t.Fatalf("expected group to be stored, got %+v", gs)
	}
	c, err := a.db.GetChat(gid.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Kind != "group" {
		t.Fatalf("expected chat kind group, got %q", c.Kind)
	}
}
