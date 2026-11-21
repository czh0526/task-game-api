-- Runs only when the compose MySQL volume is first initialized.
-- Shard databases must exist before the API connects to them; the service
-- creates the messages table itself, but not the databases.
CREATE DATABASE IF NOT EXISTS taskgame_shard_0 CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS taskgame_shard_1 CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
