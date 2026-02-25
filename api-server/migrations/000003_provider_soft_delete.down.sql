-- Restore ON DELETE CASCADE on repositories.provider_id
ALTER TABLE repositories DROP CONSTRAINT repositories_provider_id_fkey;
ALTER TABLE repositories ADD CONSTRAINT repositories_provider_id_fkey
    FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE CASCADE;

-- Remove soft delete column from providers
ALTER TABLE providers DROP COLUMN IF EXISTS deleted_at;
