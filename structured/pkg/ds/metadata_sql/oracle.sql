WITH tables_base AS (
    SELECT
        OWNER AS table_schema,
        TABLE_NAME AS table_name
    FROM ALL_TABLES
    WHERE
        OWNER = '{schema}'
), tables_with_columns AS (
    SELECT
        tb.table_schema,
        tb.table_name,
        (
            SELECT
                JSON_OBJECTAGG(
                    atc.COLUMN_NAME VALUE JSON_OBJECT(
                        'name': atc.COLUMN_NAME,
                        'default': CASE WHEN atc.DATA_DEFAULT IS NULL THEN NULL END,
                        'nullable': CASE WHEN atc.NULLABLE = 'Y' THEN 'true' ELSE 'false' END,
                        'type': UPPER(atc.DATA_TYPE),
                        'auto_increment': 'false',
                        'comment': acc.COMMENTS
                     ABSENT ON NULL)
                )
            FROM ALL_TAB_COLUMNS atc
            LEFT JOIN ALL_COL_COMMENTS acc
                ON atc.OWNER = acc.OWNER
                AND atc.TABLE_NAME = acc.TABLE_NAME
                AND atc.COLUMN_NAME = acc.COLUMN_NAME
            WHERE
                atc.OWNER = tb.table_schema AND atc.TABLE_NAME = tb.table_name
        ) AS columns
    FROM tables_base tb
), primary_keys AS (
    SELECT
        ac.OWNER AS table_schema,
        ac.TABLE_NAME AS table_name,
        JSON_ARRAYAGG(acc.COLUMN_NAME) AS primary_keys
    FROM ALL_CONSTRAINTS ac
    JOIN ALL_CONS_COLUMNS acc
        ON ac.CONSTRAINT_NAME = acc.CONSTRAINT_NAME AND ac.OWNER = acc.OWNER
    WHERE
        ac.OWNER = '{schema}' AND ac.CONSTRAINT_TYPE = 'P'
    GROUP BY
        ac.OWNER,
        ac.TABLE_NAME
), foreign_keys AS (
    SELECT
        ac.OWNER AS table_schema,
        ac.TABLE_NAME AS table_name,
        JSON_ARRAYAGG(
            JSON_OBJECT(
                'name': ac.CONSTRAINT_NAME,
                'column': acc.COLUMN_NAME,
                'referenced_schema': r_ac.OWNER,
                'referenced_table': r_ac.TABLE_NAME,
                'referenced_column': r_acc.COLUMN_NAME
             ABSENT ON NULL)
        ) AS foreign_keys
    FROM ALL_CONSTRAINTS ac
    JOIN ALL_CONS_COLUMNS acc
        ON ac.CONSTRAINT_NAME = acc.CONSTRAINT_NAME AND ac.OWNER = acc.OWNER
    JOIN ALL_CONSTRAINTS r_ac
        ON ac.R_CONSTRAINT_NAME = r_ac.CONSTRAINT_NAME
    JOIN ALL_CONS_COLUMNS r_acc
        ON r_ac.CONSTRAINT_NAME = r_acc.CONSTRAINT_NAME AND r_acc.POSITION = acc.POSITION
    WHERE
        ac.OWNER = '{schema}' AND ac.CONSTRAINT_TYPE = 'R'
    GROUP BY
        ac.OWNER,
        ac.TABLE_NAME,
        ac.CONSTRAINT_NAME,
        r_ac.OWNER,
        r_ac.TABLE_NAME
), table_comments AS (
    SELECT
        at.OWNER AS table_schema,
        at.TABLE_NAME AS table_name,
        atc.COMMENTS AS table_comment
    FROM ALL_TABLES at
    LEFT JOIN ALL_TAB_COMMENTS atc
        ON at.OWNER = atc.OWNER AND at.TABLE_NAME = atc.TABLE_NAME
    WHERE
        at.OWNER = '{schema}'
)
SELECT
    twc.table_schema,
    JSON_ARRAYAGG(
        JSON_OBJECT(
            'name': twc.table_name,
            'columns': twc.columns,
            'primary_keys': COALESCE(pk.primary_keys, JSON_ARRAY()),
            'foreign_keys': COALESCE(fk.foreign_keys, JSON_ARRAY()),
            'comment': tc.table_comment
         ABSENT ON NULL)
    ) AS tables
FROM tables_with_columns twc
LEFT JOIN primary_keys pk
    ON twc.table_schema = pk.table_schema AND twc.table_name = pk.table_name
LEFT JOIN foreign_keys fk
    ON twc.table_schema = fk.table_schema AND twc.table_name = fk.table_name
LEFT JOIN table_comments tc
    ON twc.table_schema = tc.table_schema AND twc.table_name = tc.table_name
GROUP BY
    twc.table_schema
