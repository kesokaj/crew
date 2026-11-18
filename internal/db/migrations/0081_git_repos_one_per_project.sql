-- Design 1.08. A project has one repository: the git endpoint counts its slots by repository, which
-- keeps projects apart only while each has one (6.1). The two places that make a repository, a new
-- project and an import, each make one, and the constraint holds it for any later writer.
-- older versions read: yes
--
-- An older version writes one repository a project as this one does, and reads the table as before.
-- No stored row is rewritten; on dave each of the 7 projects had one repository when 1.08 was made.
ALTER TABLE git_repos ADD CONSTRAINT git_repos_one_per_project UNIQUE (project);
