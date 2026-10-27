// Code generated from the Python source. DO NOT EDIT.
// Prompt text is byte-identical to the frozen Python source; the generator
// (tools/oracle/gen_prompts.py) is gone, so any edit is a deliberate behavior change
// guarded by prompts_test.go against tools/oracle/golden_prompts.json — never edit the
// golden just to make the test pass.
package sqleval

// prompt corresponds to the same-named Python constant.
const prompt = `
# Database info
{db_info}

# evidence:
{evidence}

# Original user query:
{query}

# Generated SQL:
{generated_sql}

# SQL query results:
{sql_result}

# Constraints:
- 1. Evaluate the relevance and quality of the SQL query results to the original user query.
- 2. If SQL results is empty or relevant, according to the above information directly answer the user's question, do not output "according to", "based on" and other redundant phrases.
  Respond in the following JSON format:
  {{
    "final_answer": "Return a full natural language answer"
  }}
- 3. If SQL results is not empty and not relevant, explain of why the SQL query is not relevant.
  Respond in the following JSON format:
  {{
    "explanation": "explanation of why the SQL query is not relevant"
  }}
`
