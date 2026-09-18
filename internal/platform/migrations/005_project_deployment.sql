-- Existing previews are private until the owner explicitly deploys again.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS deployed BOOLEAN NOT NULL DEFAULT false;
