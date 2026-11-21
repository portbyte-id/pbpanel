-- URL Rewrite presets for PHP sites (item 9).
--
-- `rewrite` names a front-controller preset that changes the panel-owned
-- php.conf `location /` line: an empty value keeps the generic default
-- (try_files ... /index.php?$query_string), `laravel` is the same query string
-- spelled explicitly, `wordpress` uses $args. A static or proxy site ignores it.

ALTER TABLE sites ADD COLUMN rewrite TEXT NOT NULL DEFAULT '';
