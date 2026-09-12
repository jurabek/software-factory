package sandbox

import "testing"

func TestOwnedWorkingPathRequiresSingletonRepositoryLocation(t *testing.T) {
	root := t.TempDir()
	if !ownedWorkingPath(root, root+"/workspace/repository") {
		t.Fatal("expected singleton repository path to be owned")
	}
	if ownedWorkingPath(root, root+"/workspace/repositories/app") {
		t.Fatal("legacy repository collection path must not be owned")
	}
}
