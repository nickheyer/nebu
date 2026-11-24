-- Create index "credential_usage_by_bucket" to table: "credential_usage"
CREATE INDEX `credential_usage_by_bucket` ON `credential_usage` (`credential_id`, `bucket`);
-- Move provider credentials of single sign-on accounts under their provider and subject. Until now an owner was the shared daemon, a local account's id or a single sign-on subject, so every owner that is neither of the first two is the third
UPDATE `provider_credentials` SET `owner` = 'oidc:' || `owner` WHERE `owner` != 'daemon' AND `owner` NOT IN (SELECT `id` FROM `users`);
-- Store the unit of budgets counting the percent of a reported window used by its number, which reads back under the name the unit had before and the one it has now
UPDATE `provider_credentials` SET `budgets` = (SELECT json_set(`provider_credentials`.`budgets`, '$.budgets', json_group_array(json(`e`.`budget`))) FROM (SELECT CASE WHEN json_extract(`b`.`value`, '$.unit') = 'BUDGET_UNIT_PERCENT_LEFT' THEN json_set(`b`.`value`, '$.unit', 5) ELSE `b`.`value` END AS `budget` FROM json_each(`provider_credentials`.`budgets`, '$.budgets') AS `b` ORDER BY `b`.`key`) AS `e`) WHERE `budgets` LIKE '%"BUDGET_UNIT_PERCENT_LEFT"%';
-- Create "credential_revocations" table
CREATE TABLE `credential_revocations` (
  `id` text NULL,
  `endpoint` text NOT NULL,
  `client_id` text NOT NULL,
  `token` text NOT NULL,
  `created_at` text NOT NULL,
  `tried_at` text NULL,
  `tries` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`)
);
