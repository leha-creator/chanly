-- Transactional invalidation journal. There is no business payload in this table.
-- +goose Up
CREATE TABLE realtime_event (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id xid8 NOT NULL DEFAULT pg_current_xact_id(),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    scope text NOT NULL CHECK (scope IN ('user','organization','admin','senior','catalog','reference')),
    audience_id uuid,
    topic text NOT NULL CHECK (topic ~ '^[a-z][a-z_.]{0,63}$'),
    object_id uuid,
    order_id uuid,
    campaign_id uuid,
    channel_id uuid,
    CHECK ((scope IN ('user','organization')) = (audience_id IS NOT NULL)),
    UNIQUE NULLS NOT DISTINCT (transaction_id,scope,audience_id,topic,object_id,order_id,campaign_id,channel_id)
);
CREATE INDEX realtime_event_transaction_idx ON realtime_event(transaction_id);
CREATE INDEX realtime_event_created_idx ON realtime_event(created_at);
-- Every pruning transaction advances a durable gap marker. Affected replicas close
-- their streams before resetting a cursor, including subscribers joining mid-recovery.
CREATE TABLE realtime_epoch (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    generation bigint NOT NULL,
    max_pruned_id bigint NOT NULL DEFAULT 0,
    max_pruned_transaction xid8 NOT NULL DEFAULT '0'
);
-- +goose StatementBegin
DO $$ DECLARE app_role text := current_setting('maxim.app_role'); BEGIN
    EXECUTE format('REVOKE UPDATE, TRUNCATE ON realtime_event FROM %I',app_role);
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION realtime_emit(t text, s text, a uuid DEFAULT NULL, obj uuid DEFAULT NULL,
    ord uuid DEFAULT NULL, camp uuid DEFAULT NULL, ch uuid DEFAULT NULL) RETURNS void
LANGUAGE sql SET search_path = public, pg_temp AS $$
    INSERT INTO realtime_event(scope,audience_id,topic,object_id,order_id,campaign_id,channel_id)
    SELECT s,a,t,obj,ord,camp,ch WHERE s NOT IN ('user','organization') OR a IS NOT NULL
    ON CONFLICT DO NOTHING;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION realtime_capture() RETURNS trigger LANGUAGE plpgsql SET search_path = public, pg_temp AS $$
DECLARE
    r jsonb; prev jsonb; obj uuid; org uuid; usr uuid; camp uuid; ord uuid; ch uuid;
    pl placement%ROWTYPE; po post%ROWTYPE; c channel%ROWTYPE;
    topic text; admin_topic text; visible boolean := false;
BEGIN
    IF TG_OP = 'DELETE' THEN r := to_jsonb(OLD); ELSE r := to_jsonb(NEW); END IF;
    IF TG_OP = 'UPDATE' THEN
        prev := to_jsonb(OLD);
        IF r = prev THEN RETURN NULL; END IF;
    END IF;
    -- placement_format has a textual ID, ord_report has a date key.
    IF TG_TABLE_NAME NOT IN ('placement_format','ord_report','ord_invoice_item','ord_invoice_creatives',
        'channel_connection_candidate','rocketwork_executor_binding','rocketwork_workflow','rocketwork_operation',
        'rocketwork_onboarding','rocketwork_withdrawal') THEN obj := (r->>'id')::uuid; END IF;
    org := (r->>'organization_id')::uuid;
    usr := (r->>'user_id')::uuid;
    camp := (r->>'campaign_id')::uuid;
    ord := (r->>'order_id')::uuid;
    ch := (r->>'channel_id')::uuid;

    CASE TG_TABLE_NAME
    WHEN 'user' THEN
        PERFORM realtime_emit('session.changed','user',obj);
        admin_topic := 'admin.organization.changed';
    WHEN 'session' THEN
        -- Activity extension is not an identity change and must not form a feedback loop.
        IF TG_OP <> 'UPDATE' OR (r->>'revoked_at') IS DISTINCT FROM (prev->>'revoked_at') THEN
            PERFORM realtime_emit('session.changed','user',usr);
        END IF;
    WHEN 'organization_member' THEN
        PERFORM realtime_emit('session.changed','user',usr);
    WHEN 'advertiser_profile','blogger_profile','legal_acceptance' THEN
        PERFORM realtime_emit('session.changed','user',m.user_id) FROM organization_member m WHERE m.organization_id=org;
        topic := 'organization.changed'; admin_topic := 'admin.organization.changed';
    WHEN 'organization' THEN
        org := obj; topic := 'organization.changed'; admin_topic := 'admin.organization.changed';
        PERFORM realtime_emit('session.changed','user',m.user_id) FROM organization_member m WHERE m.organization_id=org;
    WHEN 'notification' THEN
        IF r->>'channel' = 'cabinet' THEN PERFORM realtime_emit('notification.changed','user',usr,obj); END IF;
    WHEN 'campaign' THEN
        camp := obj; topic := 'campaign.changed';
    WHEN 'post','post_version','post_version_media' THEN
        IF TG_TABLE_NAME = 'post' THEN po := jsonb_populate_record(NULL::post,r);
        ELSIF TG_TABLE_NAME = 'post_version' THEN SELECT * INTO po FROM post WHERE id=(r->>'post_id')::uuid;
        ELSE SELECT p.* INTO po FROM post p JOIN post_version v ON v.post_id=p.id WHERE v.id=(r->>'post_version_id')::uuid;
        END IF;
        org := po.organization_id; camp := po.campaign_id; obj := po.id; topic := 'post.changed';
        FOR ord IN SELECT id FROM "order" WHERE post_id=po.id LOOP
            PERFORM realtime_emit('order.changed','organization',org,ord,ord,camp);
            PERFORM realtime_emit('admin.order.changed','admin',NULL,ord,ord,camp);
            PERFORM realtime_emit('placement.changed','organization',p.blogger_organization_id,p.id,p.order_id,camp,p.channel_id)
                FROM placement p WHERE p.order_id=ord AND p.sent_to_blogger_at IS NOT NULL;
        END LOOP;
        admin_topic := 'admin.order.changed';
    WHEN 'media_file','media_derivative' THEN
        IF TG_TABLE_NAME='media_derivative' THEN
            SELECT organization_id,id INTO org,obj FROM media_file WHERE id=(r->>'media_file_id')::uuid;
        END IF;
        topic := 'media.changed';
    WHEN 'order' THEN
        ord := obj; topic := 'order.changed'; admin_topic := 'admin.order.changed';
        PERFORM realtime_emit('placement.changed','organization',p.blogger_organization_id,p.id,ord,camp,p.channel_id)
            FROM placement p WHERE p.order_id=ord AND p.sent_to_blogger_at IS NOT NULL;
    WHEN 'placement','publication','violation','dispute','claim','dispute_message','placement_state_history','payout','hold',
         'rocketwork_workflow','rocketwork_operation','rocketwork_return_evidence','publication_views_snapshot' THEN
        IF TG_TABLE_NAME='placement' THEN pl := jsonb_populate_record(NULL::placement,r);
        ELSIF TG_TABLE_NAME='dispute_message' THEN
            SELECT p.* INTO pl FROM placement p JOIN dispute d ON d.placement_id=p.id WHERE d.id=(r->>'dispute_id')::uuid;
        ELSIF TG_TABLE_NAME='publication_views_snapshot' THEN
            SELECT p.* INTO pl FROM placement p JOIN publication pub ON pub.placement_id=p.id WHERE pub.id=(r->>'publication_id')::uuid;
        ELSIF TG_TABLE_NAME IN ('rocketwork_workflow','rocketwork_operation','rocketwork_return_evidence') THEN
            SELECT p.* INTO pl FROM placement p JOIN payout pay ON pay.placement_id=p.id WHERE pay.id=(r->>'payout_id')::uuid;
        ELSE SELECT * INTO pl FROM placement WHERE id=(r->>'placement_id')::uuid;
        END IF;
        ord := pl.order_id; ch := pl.channel_id;
        SELECT organization_id,campaign_id INTO org,camp FROM "order" WHERE id=ord;
        PERFORM realtime_emit('placement.changed','organization',org,pl.id,ord,camp,ch);
        PERFORM realtime_emit('order.changed','organization',org,ord,ord,camp);
        IF pl.sent_to_blogger_at IS NOT NULL THEN
            PERFORM realtime_emit('placement.changed','organization',pl.blogger_organization_id,pl.id,ord,camp,ch);
        END IF;
        admin_topic := 'admin.placement.changed';
        IF TG_TABLE_NAME IN ('dispute','claim','dispute_message') THEN
            PERFORM realtime_emit('admin.dispute.changed','admin',NULL,pl.id,ord,camp,ch);
        END IF;
        IF TG_TABLE_NAME IN ('payout','rocketwork_workflow','rocketwork_operation','rocketwork_return_evidence') THEN
            PERFORM realtime_emit('payout.changed','organization',pl.blogger_organization_id,coalesce((r->>'payout_id')::uuid,obj),ord,camp,ch);
            PERFORM realtime_emit('balance.changed','organization',pl.blogger_organization_id);
            PERFORM realtime_emit('admin.finance.changed','senior');
        END IF;
        IF TG_TABLE_NAME='hold' THEN PERFORM realtime_emit('balance.changed','organization',org); END IF;
        IF TG_TABLE_NAME='publication_views_snapshot' THEN PERFORM realtime_emit('admin.ord.changed','admin'); END IF;
    WHEN 'channel','channel_price','channel_metrics','channel_unavailability','channel_connection','channel_connection_candidate' THEN
        IF TG_TABLE_NAME='channel' THEN c := jsonb_populate_record(NULL::channel,r); ch := obj;
        ELSE SELECT * INTO c FROM channel WHERE id=ch; END IF;
        IF TG_TABLE_NAME IN ('channel_connection','channel_connection_candidate') THEN
            IF TG_TABLE_NAME='channel_connection_candidate' THEN
                SELECT organization_id,user_id INTO org,usr FROM channel_connection WHERE id=(r->>'connection_id')::uuid;
            END IF;
            -- Connection attempts belong to the initiating user; do not expose them to the organization.
            PERFORM realtime_emit('channel.changed','user',usr);
        ELSE
            org := c.organization_id;
            topic := CASE WHEN TG_TABLE_NAME='channel_metrics' THEN 'channel.metrics.changed' ELSE 'channel.changed' END;
            obj := ch;
            IF TG_TABLE_NAME='channel' AND prev IS NOT NULL AND (prev->>'organization_id') IS DISTINCT FROM (r->>'organization_id') THEN
                PERFORM realtime_emit(topic,'organization',(prev->>'organization_id')::uuid,ch,NULL,NULL,ch);
            END IF;
            visible := c.status='active' AND c.ord_platform_ready_at IS NOT NULL;
            IF TG_TABLE_NAME='channel' AND prev IS NOT NULL THEN
                visible := visible OR (prev->>'status'='active' AND prev->>'ord_platform_ready_at' IS NOT NULL);
            END IF;
            -- Broad catalog invalidation deliberately carries NO private object identifiers.
            IF visible THEN PERFORM realtime_emit(topic,'catalog'); END IF;
            PERFORM realtime_emit('cart.changed','organization',ci.organization_id) FROM cart_item ci WHERE ci.channel_id=ch;
        END IF;
        admin_topic := 'admin.channel.changed';
    WHEN 'cart_item' THEN topic := 'cart.changed';
    WHEN 'invoice','bank_invoice_document' THEN
        IF TG_TABLE_NAME='bank_invoice_document' THEN SELECT organization_id,id INTO org,obj FROM invoice WHERE id=(r->>'invoice_id')::uuid; END IF;
        topic := 'invoice.changed'; admin_topic := 'admin.finance.changed';
    WHEN 'closing_act' THEN topic := 'closing_act.changed'; admin_topic := 'admin.finance.changed';
    WHEN 'ledger_account' THEN topic := 'balance.changed'; obj := NULL; admin_topic := 'admin.finance.changed';
    WHEN 'ledger_transaction','ledger_entry','bank_transaction','bank_statement','admin_approval','rocketwork_withdrawal' THEN admin_topic := 'admin.finance.changed';
    WHEN 'rocketwork_contractor','rocketwork_executor_binding','rocketwork_onboarding' THEN
        topic := 'rocketwork.changed'; admin_topic := 'admin.finance.changed';
    WHEN 'channel_category' THEN PERFORM realtime_emit('category.changed','reference');
    WHEN 'placement_format' THEN PERFORM realtime_emit('format.changed','reference');
    WHEN 'audit_log' THEN admin_topic := 'admin.audit.changed'; obj := NULL;
    ELSE
        IF TG_TABLE_NAME LIKE 'ord_%' OR TG_TABLE_NAME='creative' THEN admin_topic := 'admin.ord.changed'; END IF;
    END CASE;

    IF topic IS NOT NULL AND org IS NOT NULL THEN
        PERFORM realtime_emit(topic,'organization',org,obj,ord,camp,ch);
    END IF;
    IF admin_topic IS NOT NULL THEN
        PERFORM realtime_emit(admin_topic,CASE WHEN admin_topic IN ('admin.finance.changed','admin.audit.changed') THEN 'senior' ELSE 'admin' END,
            NULL,obj,ord,camp,ch);
        PERFORM realtime_emit('admin.counters.changed','admin');
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$ DECLARE tab text; BEGIN
    FOREACH tab IN ARRAY ARRAY['user','session','organization','organization_member','advertiser_profile','blogger_profile','legal_acceptance',
        'notification','campaign','post','post_version','post_version_media','media_file','media_derivative','order','placement',
        'placement_state_history','publication','violation','dispute','claim','dispute_message','payout','hold',
        'channel','channel_price','channel_metrics','channel_unavailability','channel_connection','channel_connection_candidate','cart_item',
        'invoice','bank_invoice_document','closing_act','ledger_account','ledger_transaction','ledger_entry','bank_transaction','bank_statement',
        'admin_approval','rocketwork_contractor','rocketwork_executor_binding','rocketwork_onboarding','rocketwork_workflow',
        'rocketwork_operation','rocketwork_withdrawal','rocketwork_return_evidence','channel_category','placement_format','audit_log',
        'ord_organization','ord_contract','ord_platform','creative','ord_statistics','ord_invoice','ord_invoice_item','ord_invoice_creatives','ord_report',
        'publication_views_snapshot'] LOOP
        EXECUTE format('CREATE TRIGGER realtime_change AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION realtime_capture()',tab);
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ DECLARE tab text; BEGIN
    FOR tab IN SELECT event_object_table FROM information_schema.triggers WHERE trigger_name='realtime_change' LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS realtime_change ON %I',tab);
    END LOOP;
END $$;
-- +goose StatementEnd
DROP FUNCTION realtime_capture();
DROP FUNCTION realtime_emit(text,text,uuid,uuid,uuid,uuid,uuid);
DROP TABLE realtime_epoch;
DROP TABLE realtime_event;
