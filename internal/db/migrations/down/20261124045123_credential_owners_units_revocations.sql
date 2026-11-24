DROP TABLE `credential_revocations`;
-- The unit of a budget stays stored by its number, which the release before reads as well
UPDATE `provider_credentials` SET `owner` = substr(`owner`, 6) WHERE `owner` LIKE 'oidc:%';
DROP INDEX `credential_usage_by_bucket`;
