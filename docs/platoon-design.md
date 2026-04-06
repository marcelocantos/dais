# Jevons Platoon System Design

## Overview
A platoon of specialized agents that collectively embody the 7 key AI skills from the Nate video, integrated into Jevon's dispatch system.

## The 7 Skills & Agent Mapping
1. **Specification Precision** → Spec Agent
2. **Evaluation & Quality Judgment** → Critic/Evaluator Agent  
3. **Task Decomposition & Delegation** → Planner Agent
4. **Failure Pattern Recognition** → Debugger Agent
5. **Trust & Security Design** → Guardrail Agent
6. **Context Architecture** → Architect Agent
7. **Cost & Token Economics** → Economist Agent

## Core Components
- **Orchestrator**: Routes tasks, manages reflection loops
- **Platoon Supervisor**: Coordinates the specialized agents
- **Shared Context Bus**: Architect-managed context with proper boundaries
- **Evaluation Harness**: Critic runs automated + human-aligned evals

## Integration Points
- Extends existing `jwork` MCP tool
- Uses stateless worker dispatch (🎯T8)
- Fits into Jevon's existing agent hierarchy (overseer → product owners → bosses → workers)

## Next Steps
- Implement core platoon supervisor
- Add skill-specific agent prompts
- Build evaluation harness
- Add cost modeling layer

**Target**: 🎯T10
**Status**: identified
