-- Create "users" table
CREATE TABLE `users` (
  `id` text NULL,
  `username` text NOT NULL,
  `password_hash` text NOT NULL,
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "users_username" to table: "users"
CREATE UNIQUE INDEX `users_username` ON `users` (`username`);
