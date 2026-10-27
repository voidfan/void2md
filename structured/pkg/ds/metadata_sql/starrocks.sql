SELECT /*+ SET_VAR(group_concat_max_len = 4294967295) */
    t.table_schema,
    CONCAT(
        '[',
        GROUP_CONCAT(CONCAT(
            '{',
            '"name":"',
            REPLACE(t.table_name, '"', ''),
            '",',
            '"columns":',
            t.columns,
            ',',
            '"primary_keys":',
            COALESCE(t.primary_keys, '[]'),
            ',',
            '"foreign_keys":',
            COALESCE(t.foreign_keys, '[]'),
            ',',
            '"comment":"',
            COALESCE(REPLACE(t.table_comment, '"', ''), ''),
            '"',
            '}'
        ) SEPARATOR ','),
        ']'
    ) AS tables
FROM (
    SELECT
        t1.table_schema,
        t1.table_name,
        t1.table_comment,
        t2.columns,
        t3.primary_keys,
        t4.foreign_keys
    FROM information_schema.tables AS t1
    JOIN (
        SELECT
            t.table_schema,
            t.table_name,
            CONCAT(
                '{',
                GROUP_CONCAT(CONCAT(
                    '"',
                    REPLACE(t.column_name, '"', ''),
                    '":{',
                    '"name":"',
                    REPLACE(t.column_name, '"', ''),
                    '",',
                    '"default":',
                    CASE
                        WHEN t.column_default IS NULL
                        THEN 'null'
                        ELSE CONCAT('"', REPLACE(REPLACE(t.column_default, '"', ''), '\\', ''), '"')
                    END,
                    ',',
                    '"nullable":',
                    CASE WHEN t.is_nullable = 'YES' THEN 'true' ELSE 'false' END,
                    ',',
                    '"type":"',
                    UPPER(t.data_type),
                    '",',
                    '"auto_increment":',
                    CASE WHEN t.extra = 'auto_increment' THEN 'true' ELSE 'false' END,
                    ',',
                    '"comment":"',
                    COALESCE(REPLACE(t.column_comment, '"', ''), ''),
                    '"',
                    '}'
                ) SEPARATOR ','),
                '}'
            ) AS columns
        FROM information_schema.columns AS t
        WHERE
            table_schema = '{{db_name}}'
        GROUP BY
            t.table_schema,
            t.table_name
    ) AS t2
        ON t1.table_schema = t2.table_schema AND t1.table_name = t2.table_name
    LEFT JOIN (
        SELECT
            kcu.table_schema,
            kcu.table_name,
            CONCAT(
                '[',
                COALESCE(
                    GROUP_CONCAT(CONCAT('"', REPLACE(kcu.column_name, '"', ''), '"') SEPARATOR ','),
                    ''
                ),
                ']'
            ) AS primary_keys
        FROM information_schema.key_column_usage AS kcu
        JOIN information_schema.table_constraints AS tc
            ON kcu.table_schema = tc.table_schema
            AND kcu.table_name = tc.table_name
            AND kcu.constraint_name = tc.constraint_name
            AND tc.constraint_type = 'PRIMARY KEY'
        WHERE
            kcu.table_schema = '{{db_name}}'
        GROUP BY
            kcu.table_schema,
            kcu.table_name
    ) AS t3
        ON t1.table_schema = t3.table_schema AND t1.table_name = t3.table_name
    LEFT JOIN (
        SELECT
            kcu.table_schema,
            kcu.table_name,
            CONCAT(
                '[',
                COALESCE(
                    GROUP_CONCAT(CONCAT(
                        '{',
                        '"name":"',
                        REPLACE(kcu.constraint_name, '"', ''),
                        '",',
                        '"column":"',
                        REPLACE(kcu.column_name, '"', ''),
                        '",',
                        '"referenced_schema":',
                        CASE
                            WHEN kcu.referenced_table_schema IS NULL
                            THEN '""'
                            ELSE CONCAT('"', REPLACE(kcu.referenced_table_schema, '"', ''), '"')
                        END,
                        ',',
                        '"referenced_table":',
                        CASE
                            WHEN kcu.referenced_table_name IS NULL
                            THEN '""'
                            ELSE CONCAT('"', REPLACE(kcu.referenced_table_name, '"', ''), '"')
                        END,
                        ',',
                        '"referenced_column":',
                        CASE
                            WHEN kcu.referenced_column_name IS NULL
                            THEN '""'
                            ELSE CONCAT('"', REPLACE(kcu.referenced_column_name, '"', ''), '"')
                        END,
                        '}'
                    ) SEPARATOR ','),
                    ''
                ),
                ']'
            ) AS foreign_keys
        FROM information_schema.key_column_usage AS kcu
        JOIN information_schema.table_constraints AS tc
            ON kcu.table_schema = tc.table_schema
            AND kcu.table_name = tc.table_name
            AND kcu.constraint_name = tc.constraint_name
            AND tc.constraint_type = 'FOREIGN KEY'
        WHERE
            kcu.table_schema = '{{db_name}}'
        GROUP BY
            kcu.table_schema,
            kcu.table_name
    ) AS t4
        ON t1.table_schema = t4.table_schema AND t1.table_name = t4.table_name
    WHERE
        t1.table_schema = '{{db_name}}'
) AS t
GROUP BY
    t.table_schema
