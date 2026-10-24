CREATE TABLE "qa11_a"."order ""demo""" (
  "select" integer,
  "nama kolom" text,
  "kutip""kolom" text
);

DROP INDEX "qa11_a"."qa11_partial";

ALTER TABLE "qa11_a"."items" ADD COLUMN "catatan" text;

CREATE INDEX "qa11_partial" ON "qa11_a"."items" USING btree (id) WHERE (id > 10);