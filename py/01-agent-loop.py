import os
import subprocess

import anthropic

## NOTE: Top thing to know: first understand the message spec, all the property names are critical!!!


# The SDK will read env var by itself
client = anthropic.Client()

MODEL = 'deepseek-flash'
MAX_TOKENS = 2000
TOOLS = [
    {
        "name": "bash",
        "description": "Run a shell command.",
        "input_schema": {
            "type": "object",
            "properties": {"command": {"type": "string"}},
            "required": ["command"],
        }
    }
]

# TODO Injecting 'bash' into the system, is it a good approach?
SYSTEM_PROMPT = f"You are a coding agent at {os.getcwd()}. Use bash to solve tasks. Act, don't explain"

BASH_CMD_TIMEOUT = 120  # in secs


def execute_bash(command):
    # Skip the dangerous operations
    dangerous = ["rm -rf /", "sudo", "shutdown", "reboot", "> /dev/"]
    if any(d in command for d in dangerous):
        return "Error: Dangerous command, skipped!"

    try:
        r = subprocess.run(command, shell=True, cwd=os.getcwd(),
                           capture_output=True, text=True,
                           errors="replace", timeout=BASH_CMD_TIMEOUT)
        out = (r.stdout + r.stderr).strip()
        return out[:50000] if out else "(no output)"
    except subprocess.TimeoutExpired:
        return f"Error: Timed out ({BASH_CMD_TIMEOUT})"
    except (FileNotFoundError, OSError) as e:
        return f"Error: {e}"


def agent_loop(messages):
    while True:
        res = client.messages.create(
            model=MODEL, max_tokens=MAX_TOKENS, messages=messages,
            tools=TOOLS, system=SYSTEM_PROMPT,
        )

        # Always add the model's response content to history
        messages.append({
            "role": "assistant",
            "content": res.content,
        })

        # a python syntax sugar
        tool_calls = [block for block in res.content if block.type == "tool_use"]
        if not tool_calls:
            return

        results = []
        for block in tool_calls:
            print(f"\033[33m$ {block.input['command']}\033[0m")
            output = execute_bash(block.input["command"])
            print(output[:200])
            results.append({
                # NOTE: type is fixed to be tool_result, this is the design of anthropic message model
                "type": "tool_result",
                "tool_use_id": block.id,
                "content": output,
            })

        # NOTE: a rule: tool_result appears first:
        #  this will work
        #  results.append({"type": "text", "text": "just a test"})
        #  this will fail, complaining that tool_result and tool_use ids are not aligned
        #  results = [{"type": "text", "text": "just a test"}] + results

        # NOTE: The tool_result message's role is user, in anthropic message, only 2 roles: user and assistant
        messages.append({
            "role": "user",
            "content": results,
        })


# Press the green button in the gutter to run the script.
if __name__ == '__main__':
    # TODO A good approach is to add some stable / long enough prompts at the beginning of the history
    #  better for prompt cache, it's talked about in section 8.7 in docs/01.agent_loop.notes.md
    history = []
    while True:
        try:
            query = input("\001\033[36m\002s01 >> \001\033[0m\002")
        except (KeyboardInterrupt, EOFError):
            break

        if query.strip().lower() in ('exit', 'quit'):
            break

        history.append({
            "role": "user",
            "content": query,
        })

        # An agent loop is a round of talk, the agent application loops like this:
        # user message -> application
        # application -(delivery messages)-> model
        # application <- model res <- model
        # loop:
        #  call tool if res.content has tool_use block
        #  exit when there is no tool_use block
        # loop finished, the last item in history is the model's final answer to the user
        agent_loop(history)

        res_content = history[-1]["content"]

        if isinstance(res_content, list):
            for block in res_content:
                # NOTE: getattr is essential, the item of response.content(a list) might be ThinkingBlock, TextBlock,
                #  which is defined by anthropic API and they extend from pydantic class, so calling block['type']
                #  will fail, and serialize the history may fail either(like json.dumps(history))
                if getattr(block, "type", None) == "text":
                    print(block.text)

        print()

        # NOTE: this will fail, history has res.content, which is a list of type pydantic, not serializable
        #  way to solve it: before append to history, dump the pydantic object to normal python type
        #  exclude_none=True is essential, since some 3rd party providers like DeepSeek might not be compatible of some
        #  new fields and will fail
        #  def to_plain_blocks(content):
        #      return [block.model_dump(exclude_none=True) for block in content]

        # import json
        # history_json = json.dumps(history, indent=2)
        # print(history_json)
