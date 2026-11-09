package api

import (
	"errors"
	"net/http"
	"sort"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// A file's name is its id, so renaming moves the file, its env file and the
// operational state keyed by the id, and rewrites the references to it. The
// history already recorded (events, runs, versions) keeps the old id.

type renameRequest struct {
	NewID        string `json:"new_id"`
	ExpectedHash string `json:"expected_hash"`
}

// renamePlan describes what one kind moves along with its file.
type renamePlan struct {
	kind spec.Kind
	// env is the kind of the per-item env file to move, or "".
	env spec.Kind
	// refs is the kind flows reference this item by, or "" when none do.
	refs spec.Kind
	// assist is the assist target kind of the item, or "".
	assist string
	// moveState moves operational state keyed by the id (called before the file is renamed; undo reverses it).
	moveState func(r *http.Request, oldID, newID string) error
}

func (s *Server) renameSensor(w http.ResponseWriter, r *http.Request) {
	s.renameItem(w, r, r.PathValue("id"), renamePlan{
		kind: spec.KindSensor, env: spec.KindSensorEnv, refs: spec.KindSensor, assist: store.AssistTargetSensor,
		moveState: func(r *http.Request, oldID, newID string) error {
			return s.opts.Engine.RenameSensorState(r.Context(), oldID, newID)
		},
	}, func(newID string) (any, error) { return s.sensorDetail(r, newID) })
}

func (s *Server) renameFlow(w http.ResponseWriter, r *http.Request) {
	s.renameItem(w, r, r.PathValue("id"), renamePlan{kind: spec.KindFlow, assist: store.AssistTargetFlow},
		func(newID string) (any, error) { return s.flowDetail(newID) })
}

func (s *Server) renameTool(w http.ResponseWriter, r *http.Request) {
	s.renameItem(w, r, r.PathValue("id"), renamePlan{kind: spec.KindTool, env: spec.KindToolEnv, refs: spec.KindTool, assist: store.AssistTargetTool},
		func(newID string) (any, error) { return s.toolDetail(newID) })
}

func (s *Server) renamePrompt(w http.ResponseWriter, r *http.Request) {
	s.renameItem(w, r, r.PathValue("name"), renamePlan{kind: spec.KindPrompt, refs: spec.KindPrompt},
		func(newID string) (any, error) { return s.promptDetail(newID) })
}

func (s *Server) renameItem(w http.ResponseWriter, r *http.Request, id string, plan renamePlan, detail func(newID string) (any, error)) {
	var req renameRequest
	if !decodeJSON(w, r, &req, false) || !expectedHashOK(w, req.ExpectedHash) {
		return
	}
	newID := req.NewID
	if !checkID(w, newID) {
		return
	}
	if newID == id {
		fieldProblem(w, "new_id", "new_id is the current id")
		return
	}
	_, cur, err := s.readFile(plan.kind, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if cur != req.ExpectedHash {
		fileConflict(w, "the file no longer matches the version you loaded", req.ExpectedHash, cur)
		return
	}
	if _, _, err := s.readFile(plan.kind, newID); err == nil {
		conflictProblem(w, typeAlreadyExists, plan.kind.Rel(newID)+" already exists", nil)
		return
	} else if !errors.Is(err, spec.ErrNotFound) {
		s.writeError(w, r, err)
		return
	}
	if plan.env != "" && spec.EnvExists(s.opts.Home, plan.env, newID) {
		conflictProblem(w, typeAlreadyExists, plan.env.Rel(newID)+" already exists", nil)
		return
	}

	if plan.moveState != nil {
		if err := plan.moveState(r, id, newID); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	if _, err := spec.RenameFile(s.opts.Home, plan.kind, id, newID, req.ExpectedHash); err != nil {
		if plan.moveState != nil {
			// Put the state back under the id the file still has.
			if undo := plan.moveState(r, newID, id); undo != nil {
				s.log().Error("api: restore state after failed rename", "kind", plan.kind, "id", id, "err", undo)
			}
		}
		if errors.Is(err, spec.ErrConflict) {
			_, cur, _ := s.readFile(plan.kind, id)
			fileConflict(w, err.Error(), req.ExpectedHash, cur)
			return
		}
		s.writeError(w, r, err)
		return
	}

	changed := []string{plan.kind.Rel(id), plan.kind.Rel(newID)}
	if plan.env != "" {
		moved, err := spec.RenameEnvFile(s.opts.Home, plan.env, id, newID)
		if err != nil {
			s.log().Error("api: move env file on rename", "kind", plan.kind, "id", id, "new_id", newID, "err", err)
		}
		if moved {
			changed = append(changed, plan.env.Rel(id), plan.env.Rel(newID))
		}
	}
	if plan.refs != "" {
		changed = append(changed, s.rewriteFlowRefs(plan.refs, id, newID)...)
	}
	s.reload()
	s.PublishConfigChange(changed)
	if plan.assist != "" && s.opts.Assist != nil {
		if err := s.opts.Assist.TargetRenamed(r.Context(), plan.assist, id, newID); err != nil {
			s.log().Error("api: retarget assist sessions on rename", "kind", plan.kind, "id", id, "new_id", newID, "err", err)
		}
	}
	v, err := detail(newID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// rewriteFlowRefs rewrites flows that reference the renamed item and returns
// the paths it wrote. A flow that changed on disk meanwhile is left alone and
// logged: it keeps a dangling reference the validator reports.
func (s *Server) rewriteFlowRefs(what spec.Kind, oldID, newID string) []string {
	var names []string
	for _, f := range s.opts.Workspace().Flows {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	var paths []string
	for _, name := range names {
		raw, hash, err := s.readFile(spec.KindFlow, name)
		if err != nil {
			continue
		}
		out, changed := spec.RenameRefs(raw, what, oldID, newID)
		if !changed {
			continue
		}
		if _, err := spec.WriteFile(s.opts.Home, spec.KindFlow, name, out, hash); err != nil {
			s.log().Error("api: rewrite reference in flow", "flow", name, "kind", what, "id", oldID, "new_id", newID, "err", err)
			continue
		}
		paths = append(paths, spec.KindFlow.Rel(name))
	}
	return paths
}
