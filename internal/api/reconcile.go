package api

import (
	"context"
	"time"
)

// startStatusReconciler periodically checks servers marked "running" and flips
// them to "stopped" if their container is no longer running — so the UI reflects
// crashes/exits and the console doesn't try to attach to a dead container.
func (s *Server) startStatusReconciler() {
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for range t.C {
			s.reconcileStatuses()
		}
	}()
}

func (s *Server) reconcileStatuses() {
	rows, err := s.db.Query("SELECT id, COALESCE(container_id,'') FROM servers WHERE status='running' AND container_id<>''")
	if err != nil {
		return
	}
	type sv struct{ id, cid string }
	var list []sv
	for rows.Next() {
		var x sv
		if rows.Scan(&x.id, &x.cid) == nil {
			list = append(list, x)
		}
	}
	rows.Close()

	for _, x := range list {
		running, _, err := s.docker.State(context.Background(), x.cid)
		if err != nil {
			// Container is gone entirely — treat as stopped.
			s.db.Exec("UPDATE servers SET status='stopped' WHERE id=?", x.id)
			continue
		}
		if !running {
			s.db.Exec("UPDATE servers SET status='stopped' WHERE id=?", x.id)
		}
	}
}
