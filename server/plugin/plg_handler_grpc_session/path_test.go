package plg_handler_grpc_session

import "testing"

func TestResolvePathAllowsRootForRead(t *testing.T) {
	r, err := newPathResolver("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.resolve(".", pathUseRead)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/alice" {
		t.Fatalf("got %q", got)
	}
}

func TestResolvePathRejectsRootForMutation(t *testing.T) {
	r, err := newPathResolver("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve(".", pathUseMutateChild); err == nil {
		t.Fatal("expected root mutation to fail")
	}
}

func TestResolvePathRejectsEscape(t *testing.T) {
	r, err := newPathResolver("/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"../bob", "docs/../../bob", "/etc/passwd"} {
		if _, err := r.resolve(input, pathUseRead); err == nil {
			t.Fatalf("expected %q to fail", input)
		}
	}
}

func TestResolvePathJoinsChildren(t *testing.T) {
	r, err := newPathResolver("/home/alice/")
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.resolve("docs/report.txt", pathUseMutateChild)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/alice/docs/report.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestNewPathResolverRequiresAbsoluteRoot(t *testing.T) {
	for _, root := range []string{"", ".", "relative/root"} {
		if _, err := newPathResolver(root); err == nil {
			t.Fatalf("expected root %q to fail", root)
		}
	}
}
