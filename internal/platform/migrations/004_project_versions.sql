ALTER TABLE projects ADD COLUMN current_version_id UUID;
ALTER TABLE projects ADD COLUMN source_revision BIGINT NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN next_version_number BIGINT NOT NULL DEFAULT 1;

CREATE TABLE project_versions (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  number BIGINT NOT NULL,
  description TEXT NOT NULL,
  run_id UUID REFERENCES agent_runs(id) ON DELETE SET NULL,
  parent_version_id UUID,
  source_hash TEXT NOT NULL,
  has_thumbnail BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(project_id,number),
  UNIQUE(run_id)
);
CREATE TABLE project_restores (
  id UUID PRIMARY KEY,
  project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  version_id UUID NOT NULL,
  request_id UUID NOT NULL,
  expected_revision BIGINT NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('PENDING','RUNNING','RECOVERING','COMPLETED','FAILED','BLOCKED')),
  phase TEXT NOT NULL DEFAULT 'PREPARING',
  error_message TEXT,
  previous_runtime_running BOOLEAN NOT NULL DEFAULT false,
  previous_source_hash TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ,
  UNIQUE(project_id,request_id)
);
CREATE UNIQUE INDEX project_restores_one_active ON project_restores(project_id)
  WHERE status IN ('PENDING','RUNNING','RECOVERING','BLOCKED');
DROP INDEX agent_runs_one_active_per_project;
CREATE UNIQUE INDEX agent_runs_one_active_per_project ON agent_runs(project_id)
  WHERE status IN ('PENDING','RUNNING','VERIFYING','REPAIRING','CANCELLING');
