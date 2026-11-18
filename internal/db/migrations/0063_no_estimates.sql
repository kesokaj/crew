-- Design 0.53. No cost estimate (12.3, 12.7, 14.1).
-- older versions read: yes
--
-- The operator chose that Crew estimates no cost (2026-10-21, report 8). The price list and the share
-- by which a model was "as well, for less" are taken out of the machine's stored settings; an older
-- version fills them from its own defaults. An open recommendation for the reason "cheaper" is
-- dismissed, since no rule gives it any more; a change made for it and still watched keeps its own
-- mirror. An improvement measured by cost_per_ticket is measured by tokens_per_ticket, its metric of use
-- since 0.53, so an applied one is still measured and taken out when worse (review of 0.53).
-- runs.cost_usd and runs.cost_basis stay, unread and unwritten (16.6).
UPDATE settings SET doc = doc #- '{prices}' WHERE doc ? 'prices';
UPDATE settings SET doc = doc #- '{agents,auto,cheaper_share}' WHERE doc #> '{agents,auto,cheaper_share}' IS NOT NULL;
UPDATE model_choices SET state = 'dismissed', decided_at = now() WHERE kind = 'recommend' AND state = 'open' AND reason = 'cheaper';
UPDATE improvements SET measure = 'tokens_per_ticket' WHERE measure = 'cost_per_ticket';
