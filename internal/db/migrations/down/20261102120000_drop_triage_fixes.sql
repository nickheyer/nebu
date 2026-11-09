CREATE TABLE `instance_triage_fixes` (
  `instance_id` text NOT NULL,
  `position` integer NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`instance_id`, `position`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
