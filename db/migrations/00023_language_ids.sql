-- +goose Up

-- JavaScript was stored under the runtime's name; the bank now names the
-- language everywhere, so every column holding a language id is rewritten.
update problem set allowed_languages = array_replace(allowed_languages, 'node', 'javascript')
    where 'node' = any (allowed_languages);
update problem_reference set language = 'javascript' where language = 'node';
update submission set language = 'javascript' where language = 'node';
update attempt_source set language = 'javascript' where language = 'node';
update assessment set language_override = 'javascript' where language_override = 'node';

-- +goose Down
update problem set allowed_languages = array_replace(allowed_languages, 'javascript', 'node')
    where 'javascript' = any (allowed_languages);
update problem_reference set language = 'node' where language = 'javascript';
update submission set language = 'node' where language = 'javascript';
update attempt_source set language = 'node' where language = 'javascript';
update assessment set language_override = 'node' where language_override = 'javascript';
