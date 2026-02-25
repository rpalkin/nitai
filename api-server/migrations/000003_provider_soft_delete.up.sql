-- Add soft delete support to providers table
ALTER TABLE providers ADD COLUMN deleted_at TIMESTAMPTZ;

-- Replace ON DELETE CASCADE with ON DELETE RESTRICT on repositories.provider_id
-- since we soft-delete providers and never hard-delete them
ALTER TABLE repositories DROP CONSTRAINT repositories_provider_id_fkey;
ALTER TABLE repositories ADD CONSTRAINT repositories_provider_id_fkey
    FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE RESTRICT;
