# Dev channel prices below the production floor

**Date:** 2026-11-11
**Files:** internal/channel/domain/price.go, internal/channel/app/prices.go, internal/bootstrap/channel.go, api/openapi.yaml, migrations/00105_channel_price_dev_minimum.sql
**Severity:** medium

## Problem

A 10 RUB dev scenario was blocked by the 100 RUB channel price minimum. The price range error could orphan the ruble symbol.

## Root Cause

The floor was duplicated in frontend validation, OpenAPI, domain and PostgreSQL. Lowering the form alone would still reject persistence.

## Solution

Bootstrap selects a 1 kopeck floor only for development. The service passes it to domain validation; default and production retain 100 RUB. OpenAPI and storage permit positive kopecks, leaving environment policy in the domain. Existing save audit/logging remains, with a [FIX] success marker. Frontend uses the existing development build flag and nonbreaking currency/number spaces.

## Prevention

Cover dev and production boundaries separately and persist a 10 RUB price through the service with real PostgreSQL. Review every validation layer when changing environment policies. Zero, negative amounts and the upper limit remain checked.

## Tags

`#validation` `#environment-policy` `#postgres` `#money` `#frontend`
