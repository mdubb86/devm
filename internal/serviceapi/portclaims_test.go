package serviceapi

import "testing"

func TestPortClaims_CrossProjectConflict(t *testing.T) {
	c := newPortClaims()
	if err := c.reconcile("a", []string{"127.0.0.1:5432"}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	err := c.reconcile("b", []string{"127.0.0.1:5432"})
	if err == nil {
		t.Fatal("want conflict when project b claims a's port, got nil")
	}
	// A's claim must be intact and B must hold nothing.
	if err := c.reconcile("a", []string{"127.0.0.1:5432"}); err != nil {
		t.Fatalf("a re-claim its own port must succeed: %v", err)
	}
}

func TestPortClaims_DeclarativeReleaseOnReconcile(t *testing.T) {
	c := newPortClaims()
	c.reconcile("a", []string{"127.0.0.1:5432", "127.0.0.1:8080"})
	c.reconcile("a", []string{"127.0.0.1:5432"}) // drops 8080
	if err := c.reconcile("b", []string{"127.0.0.1:8080"}); err != nil {
		t.Fatalf("b should claim the port a released: %v", err)
	}
}

func TestPortClaims_ReleaseProject(t *testing.T) {
	c := newPortClaims()
	c.reconcile("a", []string{"127.0.0.1:5432"})
	c.release("a")
	if err := c.reconcile("b", []string{"127.0.0.1:5432"}); err != nil {
		t.Fatalf("after release, b should claim: %v", err)
	}
}
