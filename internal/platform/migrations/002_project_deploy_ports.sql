ALTER TABLE projects ADD COLUMN IF NOT EXISTS deploy_port INTEGER;

CREATE UNIQUE INDEX IF NOT EXISTS projects_deploy_port_key
  ON projects(deploy_port)
  WHERE deploy_port IS NOT NULL;
