-- Design 0.53. No limit in dollars (7.7, 14.1).
-- older versions read: yes
--
-- The operator chose that no limit is in dollars (2026-10-21, report 8). The settings of a budget are
-- taken out of the stored settings of the machine and of every project; an older version fills a
-- missing key from its own defaults. eval_suites.budget_usd gets the default 0, since this version no
-- longer writes it; an older version writes its own value. The columns of the old budgets and costs
-- stay, unread and unwritten, so a roll back reads the database: a later release drops them, once no
-- kept release reads them (16.6).
UPDATE settings SET doc = doc #- '{budget_usd}' WHERE doc ? 'budget_usd';
UPDATE settings SET doc = doc #- '{limits,run_budget_usd}' WHERE doc #> '{limits,run_budget_usd}' IS NOT NULL;
UPDATE settings SET doc = doc #- '{limits,default_task_budget_usd}' WHERE doc #> '{limits,default_task_budget_usd}' IS NOT NULL;
UPDATE settings SET doc = doc #- '{knowledge,housekeeping_budget_usd}' WHERE doc #> '{knowledge,housekeeping_budget_usd}' IS NOT NULL;
UPDATE settings SET doc = doc #- '{knowledge,evals,budget_usd}' WHERE doc #> '{knowledge,evals,budget_usd}' IS NOT NULL;
UPDATE settings SET doc = doc #- '{knowledge,evals,max_cost_increase}' WHERE doc #> '{knowledge,evals,max_cost_increase}' IS NOT NULL;
UPDATE settings SET doc = doc #- '{improvements,budget_usd_per_week}' WHERE doc #> '{improvements,budget_usd_per_week}' IS NOT NULL;
ALTER TABLE eval_suites ALTER COLUMN budget_usd SET DEFAULT 0;
