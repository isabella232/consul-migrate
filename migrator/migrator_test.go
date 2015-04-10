package migrator

import (
	"bytes"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hashicorp/raft"
)

func testRaftDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "consul-migrate")
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	// Make the mdb sub-dir
	if err := os.MkdirAll(filepath.Join(dir, "raft", "mdb"), 0700); err != nil {
		t.Fatalf("err: %s", err)
	}

	// Copy the MDB files
	for _, file := range []string{"data.mdb", "lock.mdb"} {
		src, err := os.Open(filepath.Join("test-fixtures", raftDir, mdbDir, file))
		if err != nil {
			t.Fatalf("err: %s", err)
		}

		dest, err := os.Create(filepath.Join(dir, raftDir, mdbDir, file))
		if err != nil {
			t.Fatalf("err: %s", err)
		}
		if _, err := io.Copy(dest, src); err != nil {
			t.Fatalf("err: %s", err)
		}
		src.Close()
		dest.Close()
	}

	return dir
}

func TestMigrator_new(t *testing.T) {
	// Fails on bad data-dir
	if _, err := New("/leprechauns"); err == nil {
		t.Fatalf("should fail")
	}

	// Works with an existing directory
	dir := testRaftDir(t)
	defer os.RemoveAll(dir)

	m, err := New(dir)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	// Check the paths
	if m.raftPath != filepath.Join(dir, raftDir) {
		t.Fatalf("bad: %s", m.raftPath)
	}
	if m.mdbPath != filepath.Join(dir, raftDir, mdbDir) {
		t.Fatalf("bad: %s", m.mdbPath)
	}
	if m.mdbBackupPath != filepath.Join(dir, raftDir, mdbBackupDir) {
		t.Fatalf("bad: %s", m.mdbBackupPath)
	}
	if m.boltPath != filepath.Join(dir, raftDir, boltFile) {
		t.Fatalf("bad: %s", m.boltPath)
	}
	if m.boltTempPath != filepath.Join(dir, raftDir, boltTempFile) {
		t.Fatalf("err: %s", err)
	}
}

func TestMigrator_migrate(t *testing.T) {
	dir := testRaftDir(t)
	defer os.RemoveAll(dir)

	// Create the migrator
	m, err := New(dir)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	// Perform the migration
	if _, err := m.Migrate(); err != nil {
		t.Fatalf("err: %s %s", err)
	}

	// Check that the new BoltStore was created
	if _, err := os.Stat(m.boltPath); err != nil {
		t.Fatalf("missing bolt file: %s", err)
	}

	// Check that the MDB store was backed up
	if _, err := os.Stat(m.mdbPath); err == nil {
		t.Fatalf("MDB dir was not moved")
	}
	if _, err := os.Stat(m.mdbBackupPath); err != nil {
		t.Fatalf("Missing MDB backup dir")
	}

	// Reconnect the data sources. Requires moving the MDB
	// store back to its original location.
	if err := os.Rename(m.mdbBackupPath, m.mdbPath); err != nil {
		t.Fatalf("err: %s", err)
	}

	if err := m.mdbConnect(m.raftPath); err != nil {
		t.Fatalf("err: %s", err)
	}
	defer m.mdbStore.Close()

	if err := m.boltConnect(m.boltPath); err != nil {
		t.Fatalf("err: %s", err)
	}
	defer m.boltStore.Close()

	// Check that the BoltStore now has the indexes
	mFirst, err := m.mdbStore.FirstIndex()
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	mLast, err := m.mdbStore.LastIndex()
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	bFirst, err := m.boltStore.FirstIndex()
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if bFirst != mFirst {
		t.Fatalf("bad: %d", bFirst)
	}
	bLast, err := m.boltStore.LastIndex()
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if bLast != mLast {
		t.Fatalf("bad: %d", bLast)
	}

	// Ensure that the logs were copied properly
	for i := mFirst; i <= mLast; i++ {
		mLog := &raft.Log{}
		if err := m.mdbStore.GetLog(i, mLog); err != nil {
			t.Fatalf("err: %s", err)
		}
		bLog := &raft.Log{}
		if err := m.boltStore.GetLog(i, bLog); err != nil {
			t.Fatalf("err: %s", err)
		}
		if !reflect.DeepEqual(mLog, bLog) {
			t.Fatalf("bad: %v %v", mLog, bLog)
		}
	}

	// Ensure the stable store values were copied.
	for _, key := range stableStoreKeys {
		mVal, err := m.mdbStore.Get(key)
		if err != nil {
			t.Fatalf("err: %s", err)
		}
		bVal, err := m.boltStore.Get(key)
		if err != nil {
			t.Fatalf("err: %s", err)
		}
		if !bytes.Equal(mVal, bVal) {
			t.Fatalf("bad value for key '%s'", key)
		}
	}
}