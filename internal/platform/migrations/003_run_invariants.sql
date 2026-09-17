WITH ranked_active_runs AS (
  SELECT id, row_number() OVER (PARTITION BY project_id ORDER BY started_at DESC, id DESC) AS position
  FROM agent_runs
  WHERE status IN ('PENDING', 'RUNNING', 'VERIFYING', 'REPAIRING')
)
UPDATE agent_runs
SET status = 'FAILED',
    error_message = '服务重启时检测到重复活动任务，请重新提交。',
    finished_at = now()
WHERE id IN (SELECT id FROM ranked_active_runs WHERE position > 1);

CREATE UNIQUE INDEX IF NOT EXISTS agent_runs_one_active_per_project
  ON agent_runs(project_id)
  WHERE status IN ('PENDING', 'RUNNING', 'VERIFYING', 'REPAIRING');

CREATE UNIQUE INDEX IF NOT EXISTS users_deploy_port_key
  ON users(deploy_port)
  WHERE deploy_port IS NOT NULL;
