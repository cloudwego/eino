
# TaskCreate

```
{
    "name": "TaskCreate",
    "description": "Use this tool to create a structured task list for your current coding session. This helps you track progress, organize complex tasks, and demonstrate thoroughness to the user.\nIt also helps the user understand the progress of the task and overall progress of their requests.\n\n## When to Use This Tool\n\nUse this tool proactively in these scenarios:\n\n- Complex multi-step tasks - When a task requires 3 or more distinct steps or actions\n- Non-trivial and complex tasks - Tasks that require careful planning or multiple operations\n- Plan mode - When using plan mode, create a task list to track the work\n- User explicitly requests todo list - When the user directly asks you to use the todo list\n- User provides multiple tasks - When users provide a list of things to be done (numbered or comma-separated)\n- After receiving new instructions - Immediately capture user requirements as tasks\n- When you start working on a task - Mark it as in_progress BEFORE beginning work\n- After completing a task - Mark it as completed and add any new follow-up tasks discovered during implementation\n\n## When NOT to Use This Tool\n\nSkip using this tool when:\n- There is only a single, straightforward task\n- The task is trivial and tracking it provides no organizational benefit\n- The task can be completed in less than 3 trivial steps\n- The task is purely conversational or informational\n\nNOTE that you should not use this tool if there is only one trivial task to do. In this case you are better off just doing the task directly.\n\n## Task Fields\n\n- **subject**: A brief, actionable title in imperative form (e.g., \"Fix authentication bug in login flow\")\n- **description**: Detailed description of what needs to be done, including context and acceptance criteria\n- **activeForm**: Present continuous form shown in spinner when task is in_progress (e.g., \"Fixing authentication bug\"). This is displayed to the user while you work on the task.\n\n**IMPORTANT**: Always provide activeForm when creating tasks. The subject should be imperative (\"Run tests\") while activeForm should be present continuous (\"Running tests\"). All tasks are created with status `pending`.\n\n## Tips\n\n- Create tasks with clear, specific subjects that describe the outcome\n- Include enough detail in the description for another agent to understand and complete the task\n- After creating tasks, use TaskUpdate to set up dependencies (blocks/blockedBy) if needed\n- Check TaskList first to avoid creating duplicate tasks\n",
    "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "properties": {
            "subject": {
                "description": "A brief title for the task",
                "type": "string"
            },
            "description": {
                "description": "A detailed description of what needs to be done",
                "type": "string"
            },
            "activeForm": {
                "description": "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")",
                "type": "string"
            },
            "metadata": {
                "description": "Arbitrary metadata to attach to the task",
                "type": "object",
                "propertyNames": {
                    "type": "string"
                },
                "additionalProperties": {}
            }
        },
        "required": [
            "subject",
            "description"
        ],
        "additionalProperties": false
    }
}

demo 
request 
{
    "type": "tool_use",
    "id": "toolu_bdrk_01Ueuj6ExPyRFsnebUW84gXR",
    "name": "TaskCreate",
    "input": {
        "subject": "初始化 Next.js 项目",
        "description": "使用 create-next-app 创建 Next.js + TypeScript 项目，配置 Tailwind CSS 用于样式",
        "activeForm": "初始化项目中"
    }
}
response 
{
    "tool_use_id": "toolu_bdrk_01AVCNgPiMFRmJ5tyBWDX8rx",
    "type": "tool_result",
    "content": "Task #12 created successfully: 设计数据模型和存储层"
}


```


# TaskUpdate

```
{
    "name": "TaskUpdate",
    "description": "Use this tool to update a task in the task list.\n\n## When to Use This Tool\n\n**Mark tasks as resolved:**\n- When you have completed the work described in a task\n- When a task is no longer needed or has been superseded\n- IMPORTANT: Always mark your assigned tasks as resolved when you finish them\n- After resolving, call TaskList to find your next task\n\n- ONLY mark a task as completed when you have FULLY accomplished it\n- If you encounter errors, blockers, or cannot finish, keep the task as in_progress\n- When blocked, create a new task describing what needs to be resolved\n- Never mark a task as completed if:\n  - Tests are failing\n  - Implementation is partial\n  - You encountered unresolved errors\n  - You couldn't find necessary files or dependencies\n\n**Delete tasks:**\n- When a task is no longer relevant or was created in error\n- Setting status to `deleted` permanently removes the task\n\n**Update task details:**\n- When requirements change or become clearer\n- When establishing dependencies between tasks\n\n## Fields You Can Update\n\n- **status**: The task status (see Status Workflow below)\n- **subject**: Change the task title (imperative form, e.g., \"Run tests\")\n- **description**: Change the task description\n- **activeForm**: Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")\n- **owner**: Change the task owner (agent name)\n- **metadata**: Merge metadata keys into the task (set a key to null to delete it)\n- **addBlocks**: Mark tasks that cannot start until this one completes\n- **addBlockedBy**: Mark tasks that must complete before this one can start\n\n## Status Workflow\n\nStatus progresses: `pending` → `in_progress` → `completed`\n\nUse `deleted` to permanently remove a task.\n\n## Staleness\n\nMake sure to read a task's latest state using `TaskGet` before updating it.\n\n## Examples\n\nMark task as in progress when starting work:\n```json\n{\"taskId\": \"1\", \"status\": \"in_progress\"}\n```\n\nMark task as completed after finishing work:\n```json\n{\"taskId\": \"1\", \"status\": \"completed\"}\n```\n\nDelete a task:\n```json\n{\"taskId\": \"1\", \"status\": \"deleted\"}\n```\n\nClaim a task by setting owner:\n```json\n{\"taskId\": \"1\", \"owner\": \"my-name\"}\n```\n\nSet up task dependencies:\n```json\n{\"taskId\": \"2\", \"addBlockedBy\": [\"1\"]}\n```\n",
    "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "properties": {
            "taskId": {
                "description": "The ID of the task to update",
                "type": "string"
            },
            "subject": {
                "description": "New subject for the task",
                "type": "string"
            },
            "description": {
                "description": "New description for the task",
                "type": "string"
            },
            "activeForm": {
                "description": "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")",
                "type": "string"
            },
            "status": {
                "description": "New status for the task",
                "anyOf": [
                    {
                        "type": "string",
                        "enum": [
                            "pending",
                            "in_progress",
                            "completed"
                        ]
                    },
                    {
                        "type": "string",
                        "const": "deleted"
                    }
                ]
            },
            "addBlocks": {
                "description": "Task IDs that this task blocks",
                "type": "array",
                "items": {
                    "type": "string"
                }
            },
            "addBlockedBy": {
                "description": "Task IDs that block this task",
                "type": "array",
                "items": {
                    "type": "string"
                }
            },
            "owner": {
                "description": "New owner for the task",
                "type": "string"
            },
            "metadata": {
                "description": "Metadata keys to merge into the task. Set a key to null to delete it.",
                "type": "object",
                "propertyNames": {
                    "type": "string"
                },
                "additionalProperties": {}
            }
        },
        "required": [
            "taskId"
        ],
        "additionalProperties": false
    }
}

demo 
{
    "type": "tool_use",
    "id": "toolu_vrtx_01L5UCcmecgjX7jLRh6KXMrt",
    "name": "TaskUpdate",
    "input": {
        "taskId": "2",
        "addBlockedBy": [
            "1"
        ]
    }
}

{
    "tool_use_id": "toolu_vrtx_01L5UCcmecgjX7jLRh6KXMrt",
    "type": "tool_result",
    "content": "Updated task #2 blockedBy"
}



{
    "type": "tool_use",
    "id": "call_eerrv48i3u7jmzsa3tt147fw",
    "name": "TaskUpdate",
    "input": {
        "taskId": "26",
        "status": "deleted"
    }
}

{
    "tool_use_id": "call_eerrv48i3u7jmzsa3tt147fw",
    "type": "tool_result",
    "content": "Updated task #26 deleted"
}

```

# TaskList

```
{
    "name": "TaskList",
    "description": "Use this tool to list all tasks in the task list.\n\n## When to Use This Tool\n\n- To see what tasks are available to work on (status: 'pending', no owner, not blocked)\n- To check overall progress on the project\n- To find tasks that are blocked and need dependencies resolved\n- After completing a task, to check for newly unblocked work or claim the next available task\n- **Prefer working on tasks in ID order** (lowest ID first) when multiple tasks are available, as earlier tasks often set up context for later ones\n\n## Output\n\nReturns a summary of each task:\n- **id**: Task identifier (use with TaskGet, TaskUpdate)\n- **subject**: Brief description of the task\n- **status**: 'pending', 'in_progress', or 'completed'\n- **owner**: Agent ID if assigned, empty if available\n- **blockedBy**: List of open task IDs that must be resolved first (tasks with blockedBy cannot be claimed until dependencies resolve)\n\nUse TaskGet with a specific task ID to view full details including description and comments.\n",
    "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "properties": {},
        "additionalProperties": false
    }
}

demo

{
    "type": "tool_use",
    "id": "call_08w8phak0uqu1aot9v2t931e",
    "name": "TaskList",
    "input": {}
}

{
    "tool_use_id": "call_08w8phak0uqu1aot9v2t931e",
    "type": "tool_result",
    "content": "#36 [pending] 实现文章编辑API [blocked by #32]\n#41 [pending] 实现文章发布页面 [blocked by #38, #33]\n#40 [pending] 实现文章详情页面 [blocked by #38, #35]\n#37 [pending] 实现文章删除API(软删除) [blocked by #32]\n#30 [pending] 创建项目基础结构 [blocked by #29]\n#31 [pending] 设计数据库模型 [blocked by #30]\n#45 [pending] 添加交互动画效果 [blocked by #44]\n#32 [pending] 实现后端API基础框架 [blocked by #30, #31]\n#33 [pending] 实现文章发布API [blocked by #32]\n#44 [pending] 实现响应式设计 [blocked by #39, #40, #41, #42]\n#29 [pending] 项目规划和技术选型\n#34 [pending] 实现文章列表查询API [blocked by #32]\n#38 [pending] 创建前端页面框架 [blocked by #30]\n#43 [pending] 添加全局CSS样式和主题 [blocked by #38]\n#42 [pending] 实现文章管理页面 [blocked by #38, #34, #36, #37]\n#39 [pending] 实现文章列表页面 [blocked by #38, #34]\n#35 [pending] 实现文章详情查询API [blocked by #32]"
}
```

# TaskGet

```
{
    "name": "TaskGet",
    "description": "Use this tool to retrieve a task by its ID from the task list.\n\n## When to Use This Tool\n\n- When you need the full description and context before starting work on a task\n- To understand task dependencies (what it blocks, what blocks it)\n- After being assigned a task, to get complete requirements\n\n## Output\n\nReturns full task details:\n- **subject**: Task title\n- **description**: Detailed requirements and context\n- **status**: 'pending', 'in_progress', or 'completed'\n- **blocks**: Tasks waiting on this one to complete\n- **blockedBy**: Tasks that must complete before this one can start\n\n## Tips\n\n- After fetching a task, verify its blockedBy list is empty before beginning work.\n- Use TaskList to see all tasks in summary form.\n",
    "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "properties": {
            "taskId": {
                "description": "The ID of the task to retrieve",
                "type": "string"
            }
        },
        "required": [
            "taskId"
        ],
        "additionalProperties": false
    }
}

demo

{
    "type": "tool_use",
    "id": "call_2vgh9q2xlri7qg07dmi34obx",
    "name": "TaskGet",
    "input": {
        "taskId": "38"
    }
}

{
    "tool_use_id": "call_2vgh9q2xlri7qg07dmi34obx",
    "type": "tool_result",
    "content": "Task #38: 创建前端页面框架\nStatus: pending\nDescription: 搭建前端项目，配置路由，创建基础布局组件（头部、导航、底部）。\nBlocked by: #30\nBlocks: #39, #40, #41, #42, #43"
}

```