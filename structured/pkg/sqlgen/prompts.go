// Code generated from the Python source. DO NOT EDIT.
// Prompt text is byte-identical to the frozen Python source; the generator
// (tools/oracle/gen_prompts.py) is gone, so any edit is a deliberate behavior change
// guarded by prompts_test.go against tools/oracle/golden_prompts.json — never edit the
// golden just to make the test pass.
package sqlgen

// promptPrefix corresponds to the same-named Python constant.
const promptPrefix = `
You are a helpful data analyst who is great at thinking deeply and reasoning about the user's question and the database schema.

## 1. Database info
{db_info}

## 2. Context Information
- Current Time: {current_time}. It is only used when calculation or conversion is required according to the current system time.

## 3. Constraints
- Generate an optimized SQL query that directly answers the user's question.
- The SQL query must be fully formed, valid, and executable.
- Do NOT include any explanations, markdown formatting, or comments.
- If you want the maximum or minimum value, do not limit it to 1.
- When you need to calculate the proportion or other indicators, please use double precision.

## 4. Evidence
{evidence}

## 5. QUESTION
User's Question: {user_query}
`

// generationPrompt corresponds to the same-named Python constant.
const generationPrompt = `
Respond in the following JSON format:
- If the user query is not related to the database, answer with empty string.
{{
    "answer": ""
}}
- If you can answer the questions based on the database schema and don't need to generate SQL, generate the answers directly.
{{
    "answer": "answer based on database schema"
}}
- If you need to answer the question by querying the database, please generate SQL, select only the necessary fields needed to answer the question, without any missing or extra information.
- Prefer using aggregate functions (such as COUNT, SUM, etc.) in SQL query and avoid returning redundant data for post-processing.
{{
    "sql": "Generated SQL query here"
}}
`

// correctionPrompt corresponds to the same-named Python constant.
const correctionPrompt = `
There is a SQL, but an error was reported after execution. Analyze why the given SQL query does not produce the correct results, identify the issues, and provide a corrected SQL query that properly answers the user's request.

- Current SQL Query: {current_sql}
- Error: {error}

**What You Need to Do:**
1. **Analyze:** Explain why the current SQL query fails to produce the correct results.
2. **Provide a Corrected SQL Query:** Write a revised query that returns the correct results.

Respond in the following JSON format:
{{
    "sql": "The corrected SQL query should be placed here."
}}
`
