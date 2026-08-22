package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSavedBypassRoomsPreservesConfiguredRooms(t *testing.T) {
	groupsDir := filepath.Join(t.TempDir(), "groups")
	for name, call := range map[string]string{
		"7":       "\nwbstream://room-seven\n",
		"12":      "wbstream://old\n\nwbstream://room-twelve\n",
		"invalid": "wbstream://ignored\n",
		"20":      "\n",
	} {
		dir := filepath.Join(groupsDir, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "call.txt"), []byte(call), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rooms, err := savedBypassRooms(groupsDir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, room := range rooms {
		got[room.groupID] = room.room
	}
	if len(got) != 2 || got[7] != "wbstream://room-seven" || got[12] != "wbstream://room-twelve" {
		t.Fatalf("unexpected saved rooms: %#v", got)
	}
}

func TestSavedBypassRoomsAllowsEmptyInstall(t *testing.T) {
	rooms, err := savedBypassRooms(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 0 {
		t.Fatalf("expected no saved rooms, got %#v", rooms)
	}
}

func TestListSavedBypassRoomsReturnsCodesForEveryProvider(t *testing.T) {
	root := t.TempDir()
	cases := map[string]string{
		"wb":       "wbstream://019ff9ed-eb9a-76f2-bd37-295faadf094c",
		"telemost": "https://telemost.yandex.ru/j/9648507117418?from=panel",
		"dion":     "dion://room-dion#fragment",
		"vk":       "https://calls.vk.com/join/room-vk/",
	}
	for provider, room := range cases {
		dir := filepath.Join(root, "bypass-"+provider, "groups", "7")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "call.txt"), []byte(room+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rooms, err := listSavedBypassRooms(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, room := range rooms {
		if room.GroupID != 7 {
			t.Fatalf("unexpected group: %#v", room)
		}
		got[room.Provider] = room.Code
	}
	want := map[string]string{"wbstream": "019ff9ed-eb9a-76f2-bd37-295faadf094c", "telemost": "9648507117418", "dion": "room-dion", "vk": "room-vk"}
	for provider, code := range want {
		if got[provider] != code {
			t.Fatalf("%s code = %q, want %q; rooms: %#v", provider, got[provider], code, rooms)
		}
	}
}
