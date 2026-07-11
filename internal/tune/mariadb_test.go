package tune

import (
	"strings"
	"testing"
)

func TestBufferPoolByRole(t *testing.T) {
	shared := CalculateMariaDB(8192, 4, RoleShared, false)
	if shared.BufferPoolMB != 2048 { // 25% of 8 GB
		t.Errorf("shared pool = %d, want 2048", shared.BufferPoolMB)
	}
	dedicated := CalculateMariaDB(8192, 4, RoleDedicated, false)
	if dedicated.BufferPoolMB != 5734 { // 70% of 8 GB
		t.Errorf("dedicated pool = %d, want 5734", dedicated.BufferPoolMB)
	}
	tiny := CalculateMariaDB(512, 1, RoleShared, false)
	if tiny.BufferPoolMB < 128 {
		t.Errorf("tiny pool = %d, want floor of 128", tiny.BufferPoolMB)
	}
}

func TestCommerceDurability(t *testing.T) {
	if c := CalculateMariaDB(4096, 2, RoleShared, true); c.FlushLogAtCommit != 1 {
		t.Error("commerce must use full durability (flush=1)")
	}
	if c := CalculateMariaDB(4096, 2, RoleShared, false); c.FlushLogAtCommit != 2 {
		t.Error("non-commerce defaults to performance flush (2)")
	}
}

func TestRenderConfig(t *testing.T) {
	c := CalculateMariaDB(4096, 4, RoleShared, false)
	out, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"innodb_buffer_pool_size = 1024M",
		"max_connections = 200",
		"slow_query_log = 1",
		"innodb_flush_method = O_DIRECT",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config missing %q", want)
		}
	}
}
