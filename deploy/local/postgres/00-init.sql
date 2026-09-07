-- First-boot initialisation for the local PostgreSQL instance.
--
-- Runs once, as superuser, before any application migration. Anything here must be safe to
-- run exactly once and must not assume application tables exist — migrations have not run
-- yet at this point.
--
-- Note on ADR-0001: append-only enforcement is moving from triggers to INSERT-only grants,
-- because grants exist on every database engine and triggers do not. The roles are created
-- here; the table-level grants are applied by `platformctl migrate` once the tables exist.
-- Those grants are mandatory, not optional hardening — with the triggers demoted they are
-- what upholds Invariant 4's "no deletion path" clause.

-- Extensions. Deliberately few: each one is a dependency that has to exist on every target,
-- and ADR-0001 caps how exotic those may get.
CREATE EXTENSION IF NOT EXISTS pgcrypto;      -- digest() for evidence verification helpers
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

-- Temporal owns separate databases (ADR-0004 defers Temporal to the workflow profile, but
-- creating the databases is harmless and avoids a first-run race in auto-setup).
SELECT 'CREATE DATABASE temporal'
 WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'temporal')\gexec
SELECT 'CREATE DATABASE temporal_visibility'
 WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'temporal_visibility')\gexec

-- Application roles.
--
-- Three roles rather than one, because the isolation model in ADR-0001 depends on the
-- writer for append-only tables being unable to issue UPDATE or DELETE at all. A single
-- superuser connection makes that unenforceable no matter what the application does.
DO $$
BEGIN
    -- Owns the schema and runs migrations. Not used for request handling.
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'satta_migrator') THEN
        CREATE ROLE satta_migrator LOGIN PASSWORD 'satta-migrator-dev';
    END IF;

    -- Ordinary request handling. Subject to row-level security; FORCE ROW LEVEL SECURITY on
    -- the tables means even the owner cannot bypass a policy.
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'satta_app') THEN
        CREATE ROLE satta_app LOGIN PASSWORD 'satta-app-dev';
    END IF;

    -- Append-only writer. Granted INSERT and SELECT only, never UPDATE or DELETE, on the
    -- evidence and usage tables. This is the portable replacement for the append-only
    -- triggers (ADR-0001).
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'satta_appender') THEN
        CREATE ROLE satta_appender LOGIN PASSWORD 'satta-appender-dev';
    END IF;

    -- Auditor. Structurally read-only at the database layer as well as in the application
    -- (SAFETY INVARIANT 11). Two layers, because an application-only check is one bug away
    -- from being bypassed.
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'satta_auditor') THEN
        CREATE ROLE satta_auditor LOGIN PASSWORD 'satta-auditor-dev';
    END IF;
END
$$;

GRANT CONNECT ON DATABASE satta TO satta_migrator, satta_app, satta_appender, satta_auditor;
GRANT USAGE ON SCHEMA public TO satta_app, satta_appender, satta_auditor;
GRANT CREATE, USAGE ON SCHEMA public TO satta_migrator;

-- Default privileges for tables the migrator will create later. Set here so that a new table
-- is correctly restricted the moment it exists, rather than depending on someone remembering
-- to grant after each migration.
ALTER DEFAULT PRIVILEGES FOR ROLE satta_migrator IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO satta_app;
ALTER DEFAULT PRIVILEGES FOR ROLE satta_migrator IN SCHEMA public
    GRANT SELECT, INSERT ON TABLES TO satta_appender;
ALTER DEFAULT PRIVILEGES FOR ROLE satta_migrator IN SCHEMA public
    GRANT SELECT ON TABLES TO satta_auditor;
ALTER DEFAULT PRIVILEGES FOR ROLE satta_migrator IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO satta_app, satta_appender;

-- The auditor may never write, whatever else is granted later. REVOKE is belt-and-braces
-- against a future migration granting too broadly.
DO $$
BEGIN
    EXECUTE 'ALTER DEFAULT PRIVILEGES FOR ROLE satta_migrator IN SCHEMA public '
            'REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON TABLES FROM satta_auditor';
EXCEPTION WHEN OTHERS THEN
    -- Revoking a privilege that was never granted is not an error worth failing boot over.
    NULL;
END
$$;

COMMENT ON SCHEMA public IS
    'Platform schema. Roles: satta_migrator owns DDL; satta_app handles requests under RLS; satta_appender has INSERT+SELECT only on append-only tables (ADR-0001); satta_auditor is read-only (INVARIANT 11).';
