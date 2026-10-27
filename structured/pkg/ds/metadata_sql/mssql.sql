SELECT 
    t.table_schema,
    '[' + STUFF((
        SELECT ',' + '{"name":"' + t1.table_name + '","schema":"' + t1.table_schema + '","columns":' + t1.columns + 
               ',"primary_keys":' + ISNULL(t1.primary_keys, '[]') + 
               ',"foreign_keys":' + ISNULL(t1.foreign_keys, '[]') + 
               ',"comment":"' + REPLACE(ISNULL(t1.comment, ''), '"', '\"') + '"}'
        FROM (
            SELECT
                t1.table_schema,
                t1.table_name,
                t1.comment,
                t2.columns,
                t3.primary_keys,
                t4.foreign_keys
            FROM (
                SELECT
                    s.name AS table_schema,
                    t.name AS table_name,
                    ISNULL(CONVERT(NVARCHAR(MAX), ep.value), '') AS comment
                FROM sys.tables t
                JOIN sys.schemas s ON t.schema_id = s.schema_id
                LEFT JOIN sys.extended_properties ep ON ep.major_id = t.object_id 
                    AND ep.minor_id = 0 
                    AND ep.class = 1
                    AND ep.name = 'MS_Description'
                WHERE s.name = '{{schema}}'
            ) AS t1
            JOIN (
                SELECT
                    s.name AS table_schema,
                    t.name AS table_name,
                    '{' + STUFF((
                        SELECT ',' + '"' + c.name + '":{"name":"' + c.name + '","default":' + 
                               CASE 
                                   WHEN c.default_object_id = 0 THEN 'null' 
                                   ELSE '"' + REPLACE(REPLACE(CONVERT(NVARCHAR(MAX), dc.definition), '"', '\"'), '\', '\\') + '"' 
                               END + 
                               ',"nullable":' + CASE WHEN c.is_nullable = 1 THEN 'true' ELSE 'false' END + 
                               ',"type":"' + UPPER(tt.name) + '"' +
                               ',"auto_increment":' + CASE WHEN c.is_identity = 1 THEN 'true' ELSE 'false' END + 
                               ',"comment":"' + REPLACE(ISNULL(CONVERT(NVARCHAR(MAX), ep_col.value), ''), '"', '\"') + '"}'
                        FROM sys.columns c
                        JOIN sys.types tt ON c.user_type_id = tt.user_type_id
                        LEFT JOIN sys.default_constraints dc ON c.default_object_id = dc.object_id
                        LEFT JOIN sys.extended_properties ep_col ON ep_col.major_id = t.object_id 
                            AND ep_col.minor_id = c.column_id 
                            AND ep_col.class = 1
                            AND ep_col.name = 'MS_Description'
                        WHERE c.object_id = t.object_id
                        FOR XML PATH(''), TYPE
                    ).value('.', 'NVARCHAR(MAX)'), 1, 1, '') + '}' AS columns
                FROM sys.tables t
                JOIN sys.schemas s ON t.schema_id = s.schema_id
                WHERE s.name = '{{schema}}'
            ) AS t2 ON t1.table_schema = t2.table_schema AND t1.table_name = t2.table_name
            LEFT JOIN (
                SELECT
                    s.name AS table_schema,
                    t.name AS table_name,
                    '[' + STUFF((
                        SELECT ',"' + c.name + '"'
                        FROM sys.index_columns ic
                        JOIN sys.columns c ON ic.object_id = c.object_id AND ic.column_id = c.column_id
                        JOIN sys.indexes i ON ic.object_id = i.object_id AND ic.index_id = i.index_id
                        WHERE ic.object_id = t.object_id AND i.is_primary_key = 1
                        FOR XML PATH(''), TYPE
                    ).value('.', 'NVARCHAR(MAX)'), 1, 1, '') + ']' AS primary_keys
                FROM sys.tables t
                JOIN sys.schemas s ON t.schema_id = s.schema_id
                WHERE s.name = '{{schema}}'
            ) AS t3 ON t1.table_schema = t3.table_schema AND t1.table_name = t3.table_name
            LEFT JOIN (
                SELECT
                    s.name AS table_schema,
                    t.name AS table_name,
                    '[' + STUFF((
                        SELECT ',' + '{"name":"' + fk.name + '","column":"' + c.name + 
                               '","referenced_schema":"' + rs.name + 
                               '","referenced_table":"' + rt.name + 
                               '","referenced_column":"' + rc.name + '"}'
                        FROM sys.foreign_keys fk
                        JOIN sys.foreign_key_columns fkc ON fk.object_id = fkc.constraint_object_id
                        JOIN sys.columns c ON fkc.parent_object_id = c.object_id AND fkc.parent_column_id = c.column_id
                        JOIN sys.tables rt ON fkc.referenced_object_id = rt.object_id
                        JOIN sys.schemas rs ON rt.schema_id = rs.schema_id
                        JOIN sys.columns rc ON fkc.referenced_object_id = rc.object_id AND fkc.referenced_column_id = rc.column_id
                        WHERE fk.parent_object_id = t.object_id
                        FOR XML PATH(''), TYPE
                    ).value('.', 'NVARCHAR(MAX)'), 1, 1, '') + ']' AS foreign_keys
                FROM sys.tables t
                JOIN sys.schemas s ON t.schema_id = s.schema_id
                WHERE s.name = '{{schema}}'
            ) AS t4 ON t1.table_schema = t4.table_schema AND t1.table_name = t4.table_name
        ) AS t1
        FOR XML PATH(''), TYPE
    ).value('.', 'NVARCHAR(MAX)'), 1, 1, '') + ']' AS tables
FROM (
    SELECT DISTINCT s.name AS table_schema
    FROM sys.tables t
    JOIN sys.schemas s ON t.schema_id = s.schema_id
    WHERE s.name = '{{schema}}'
) AS t