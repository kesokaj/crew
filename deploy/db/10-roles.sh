#!/bin/sh
# Makes Crew's database and its three roles, once, when crew-db starts on an empty data folder
# (design 15.3). The image runs every file in /docker-entrypoint-initdb.d at that moment only.
#   crew          the server: reads and writes rows
#   crew_migrate  owns the schema, and is the only role that changes it (crew admin migrate, restore)
#   crew_read     reads only: crew-ctl db shell, and the backups
# The passwords are Docker secrets that crew-ctl makes at install. psql reads each file itself, so no
# password is on a command line.
set -eu

S="${CREW_SECRETS_DIR:-/run/secrets}"
for s in db_crew db_crew_migrate db_crew_read; do
    [ -s "$S/$s" ] || { echo "10-roles.sh: the secret $s is missing or empty" >&2; exit 1; }
done

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres -v "secrets=$S" <<'SQL'
\set pw_server `tr -d '\n' < :secrets/db_crew`
\set pw_migrate `tr -d '\n' < :secrets/db_crew_migrate`
\set pw_read `tr -d '\n' < :secrets/db_crew_read`
CREATE ROLE crew_migrate LOGIN PASSWORD :'pw_migrate';
CREATE ROLE crew LOGIN PASSWORD :'pw_server';
CREATE ROLE crew_read LOGIN PASSWORD :'pw_read';
CREATE DATABASE crew OWNER crew_migrate;
REVOKE CONNECT, TEMPORARY ON DATABASE crew FROM PUBLIC;
GRANT CONNECT ON DATABASE crew TO crew, crew_migrate, crew_read;
\connect crew
REVOKE ALL ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO crew_migrate;
GRANT USAGE ON SCHEMA public TO crew, crew_read;
ALTER DEFAULT PRIVILEGES FOR ROLE crew_migrate IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO crew;
ALTER DEFAULT PRIVILEGES FOR ROLE crew_migrate IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO crew;
ALTER DEFAULT PRIVILEGES FOR ROLE crew_migrate IN SCHEMA public
    GRANT SELECT ON TABLES TO crew_read;
ALTER DEFAULT PRIVILEGES FOR ROLE crew_migrate IN SCHEMA public
    GRANT SELECT ON SEQUENCES TO crew_read;
SQL
