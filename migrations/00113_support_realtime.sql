-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION support_realtime_capture() RETURNS trigger LANGUAGE plpgsql SET search_path=public,pg_temp AS $$
DECLARE r jsonb; conv uuid;
BEGIN
 IF TG_OP='DELETE' THEN r:=to_jsonb(OLD); ELSE r:=to_jsonb(NEW); END IF;
 IF TG_TABLE_NAME='support_conversation' THEN conv:=(r->>'id')::uuid;
 ELSIF TG_TABLE_NAME='support_attachment' THEN SELECT conversation_id INTO conv FROM support_message WHERE id=(r->>'message_id')::uuid;
 ELSE conv:=(r->>'conversation_id')::uuid;
 END IF;
 IF TG_TABLE_NAME='support_read_marker' THEN
   PERFORM realtime_emit('admin.support.read','user',(r->>'actor_id')::uuid,conv);
 ELSIF conv IS NOT NULL THEN
   PERFORM realtime_emit('admin.support.changed','admin',NULL,conv);
   PERFORM realtime_emit('admin.counters.changed','admin');
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER support_conversation_realtime AFTER INSERT OR UPDATE OR DELETE ON support_conversation FOR EACH ROW EXECUTE FUNCTION support_realtime_capture();
CREATE TRIGGER support_message_realtime AFTER INSERT OR UPDATE OR DELETE ON support_message FOR EACH ROW EXECUTE FUNCTION support_realtime_capture();
CREATE TRIGGER support_attachment_realtime AFTER INSERT OR UPDATE OR DELETE ON support_attachment FOR EACH ROW EXECUTE FUNCTION support_realtime_capture();
CREATE TRIGGER support_read_realtime AFTER INSERT OR UPDATE OR DELETE ON support_read_marker FOR EACH ROW EXECUTE FUNCTION support_realtime_capture();

-- +goose Down
DROP TRIGGER support_conversation_realtime ON support_conversation;
DROP TRIGGER support_message_realtime ON support_message;
DROP TRIGGER support_attachment_realtime ON support_attachment;
DROP TRIGGER support_read_realtime ON support_read_marker;
DROP FUNCTION support_realtime_capture();
