-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION reject_immutable_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'immutable history cannot be changed'
        USING ERRCODE = '23514', CONSTRAINT = 'immutable_history';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION require_next_authority_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.suite_id IS DISTINCT FROM OLD.suite_id THEN
        RAISE EXCEPTION 'Suite authority identity cannot be changed'
            USING ERRCODE = '23514', CONSTRAINT = 'suites_identity_immutable';
    END IF;
    IF NEW.authority_revision <> OLD.authority_revision + 1 THEN
        RAISE EXCEPTION 'Suite authority writes require the exact next revision'
            USING ERRCODE = '23514', CONSTRAINT = 'suites_revision_advance';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER suites_revision_guard BEFORE UPDATE ON suites
    FOR EACH ROW EXECUTE FUNCTION require_next_authority_revision();
CREATE TRIGGER suites_delete_guard BEFORE DELETE ON suites
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER suites_truncate_guard BEFORE TRUNCATE ON suites
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();

CREATE TRIGGER suite_versions_immutable BEFORE UPDATE OR DELETE ON suite_versions
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER suite_versions_no_truncate BEFORE TRUNCATE ON suite_versions
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER operation_receipts_immutable BEFORE UPDATE OR DELETE ON operation_receipts
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER operation_receipts_no_truncate BEFORE TRUNCATE ON operation_receipts
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER consent_sources_immutable BEFORE UPDATE OR DELETE ON consent_sources
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER consent_sources_no_truncate BEFORE TRUNCATE ON consent_sources
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER promotions_immutable BEFORE UPDATE OR DELETE ON promotions
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER promotions_no_truncate BEFORE TRUNCATE ON promotions
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER audit_events_immutable BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER publication_intents_immutable BEFORE UPDATE OR DELETE ON publication_intents
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER publication_intents_no_truncate BEFORE TRUNCATE ON publication_intents
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER consent_acknowledgments_immutable BEFORE UPDATE OR DELETE ON consent_acknowledgments
    FOR EACH ROW EXECUTE FUNCTION reject_immutable_history();
CREATE TRIGGER consent_acknowledgments_no_truncate BEFORE TRUNCATE ON consent_acknowledgments
    FOR EACH STATEMENT EXECUTE FUNCTION reject_immutable_history();
