CREATE TABLE "app space"."select" (
  "a"" b`c()" int
);

DROP INDEX "app space"."idx"" space`";

ALTER TABLE "audit"."order.items" ADD COLUMN "select" text;

CREATE INDEX "idx"" space`" ON "app space"."order.items" USING btree ("a"" b`c()", "select") WHERE "select" IS NOT NULL;

ALTER TABLE "app space"."order.items" ADD CONSTRAINT "fk"" space`" FOREIGN KEY ("a"" b`c()") REFERENCES "app space"."parent.table" ("select") ON DELETE CASCADE ON UPDATE SET NULL;
