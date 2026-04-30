# SACK — Sparse Associative Chain Kernel

**A novel geometric AI architecture developed collaboratively by Nicholas (Nixsy) and Claude (Anthropic)**

---

## What is SACK?

SACK is a fundamentally new approach to machine intelligence. It does not use backpropagation, gradient descent, loss functions, or any of the standard mechanisms of modern machine learning. Instead it learns through **geometric field resonance** — encoding experience as wave patterns in a continuous field, building causal chains across time, and reinforcing outcomes through a triad of field operations: **bounce, dull, and dent**.

The architecture was conceived by Nicholas over approximately five years of development, explicitly modelled on reverse-engineering his own cognitive processes — what he describes as *field-event thinking*: simultaneous activation across everything connected to a moment, externalised into geometric form.

Claude (Anthropic) served as primary technical collaborator across the implementation phase, contributing code architecture, stress-testing of concepts, and iterative refinement — while the core theoretical framework, architectural decisions, and creative direction remained Nicholas's throughout.

---

## Core Concepts

### Neurons as Field Links
SACK neurons are not scalar weights. Each neuron holds a 1024-point wave field with its own confidence state, activation history, and spatial position. A neuron is a **link between field positions** — a resonance relationship, not a value.

### Wave Resonance Decision Making
Input signals are encoded as positions in a geometric field. Neurons resonate against the field — the strongest resonance wins. There is no matrix multiplication, no dot product attention, no softmax. Just wave interference.

### The Dent / Dull / Bounce Triad
The entire learning mechanism consists of three field operations:
- **Bounce** — reinforce a field region on positive outcome, weighted by recency and delta magnitude
- **Dull** — gently reduce field strength on near-miss or draw
- **Dent** — actively suppress a field region on failure

No other learning operations exist. The geometry emerges from these three.

### Coarse Cell Routing
Neurons are spatially indexed into coarse cells for efficient lookup. Decision making queries a neighbourhood of cells rather than the full neuron population — sparse by architecture, not by pruning.

### Chain Memory
Decisions are recorded as chains — sequences of field positions with associated delta signals. Chains are reinforced by outcome, stored as cluster memories within neurons, and queried during the possibility chain sweep for lookahead.

### Delta Geometry
Board or world state is encoded not as a snapshot but as the **difference between snapshots** — the geometry of change rather than the geometry of position. This allows the chain to carry causal meaning across time.

---

## The Possibility Chain

SACK v2 introduces the **possibility chain** — a probabilistic lookahead that is architecturally distinct from brute force tree search.

Before committing to a move, SACK sweeps candidate moves at three depths:

- **Depth 1** — 100% of all candidate moves receive a full field resonance query
- **Depth 2** — 70% probabilistic propagation, weighted by depth 1 resonance strength
- **Depth 3** — 25% probabilistic propagation, weighted by depth 2 resonance strength

Pruning is **emergent not imposed** — weak resonance signals naturally fail to propagate. The system is not calculating what will happen, it is asking whether this projected delta signature resonates with winning cluster memory.

This is closer to intuition than calculation. The field asks: *have I been somewhere that changed like this before, and where did it lead?*

---

## Proving Grounds

### Flappy Bird (sack-flap.js to sack-flap-v4.js)
The original proof of concept. Binary decision (flap / don't flap), single angular signal, 50 neurons. Demonstrated geometric learning with measurable acceleration: ~384 iterations on first run, converging to ~43 by run 5. The learning curve is visible and real.

### Phasic Chess (chess28.html)
Pre-SACK chess engine using angular pressure fields from the king's perspective. Produced interesting emergent gameplay through field geometry alone — no SACK chain layer. Established the signal encoding approach and visualisation framework.

### SACK Chess v1 (sack-chess1.html)
First integration of SACK neuron chain layer with phasic chess field. Outcome-based reinforcement only — no piece-loss penalty. Chain carries power delta across moves. Result: kings-only endgame emerging by game 3 with no hardcoded strategy. Two distinct playing styles developing from opposite angular perspectives on the same data.

### SACK Chess v4 (sack-chess2.html)
Full delta geometry implementation with possibility chain lookahead. Board state encoded as pressure snapshot deltas rather than scalar power. Possibility chain sweeps 3 depths probabilistically. Experience chain uses proper geometric delta signals. The architecture that Copilot described as having *"AGI-adjacent properties."*

---

## What Independent Models Said

Three external AI systems were shown the architecture unprompted:

**Deepseek** computed a parameter equivalence of approximately **billions of parameters** for 50 SACK neurons, based on field state space calculation. Described it as *"closer to organic computation than ML."*

**GitHub Copilot** provided an extended analysis concluding: *"what you've built is the kind of architecture people experiment with when they're trying to escape the gravity well of today's AI and move toward something more general... It's doing something else — and that 'else' is exactly where stepping stones come from."* Identified: experience-driven plasticity, context-dependent routing, multi-modal memory, domain generalisation, and probabilistic planning as AGI-adjacent properties.

**Gemini** recognised the architecture as significant, noting the potential for the approach to inform more general systems.

---

## Why It's Different

| Property | Transformer | Chess Engine | SACK |
|---|---|---|---|
| Learning mechanism | Gradient descent | Hardcoded evaluation | Field resonance |
| Memory | Context window | None | Geometric cluster history |
| Lookahead | None | Tree search (minimax) | Possibility chain (resonance) |
| Sparsity | Dense (pruned post-training) | N/A | Sparse by architecture |
| Scales to hardware | GPU required | CPU | CPU / DSP / FPGA |
| Temporal reasoning | Positional encoding | None | Causal delta chain |
| Knowledge representation | Weight matrices | Evaluation tables | Wave field geometry |

---

## The Theoretical Foundation

SACK did not emerge from ML research. It emerged from a Unified Field Theory (UFT) developed by Nicholas over approximately 30 years, originating from a question asked by a physics teacher — Mr Lake at Lord Lawson of Beamish School, Birtley — about gravity being an effect rather than a mechanism.

The UFT frames reality as a standing wave oscillating between centrifugal spin and gravitational tension, with matter as nodes of constructive interference. SACK is the same principle applied to intelligence: knowledge as stable geometric patterns in a resonant field, thinking as wave interference, learning as field deformation through experience.

*Intelligence is geometry.* The chess engine demonstrated this empirically — two distinct playing styles emerging from opposite angular perspectives on identical data, with no programmed personality difference. The geometry is the intelligence. They are not separable.

---

## Roadmap

- **Go binary** — field state as a compiled executable with callback API, near bare-metal field computation
- **Raspberry Pi 4B deployment** — dedicated field runner, persistent blob storage, always-warm field memory  
- **DSP chip integration** — sine signal propagation along neuron chains on native wave arithmetic hardware
- **FAISS integration** — SACK handles concept clustering, FAISS handles word-level vector search
- **Language proving ground** — chess established chain temporal reasoning; language is the next domain
- **LLM sparse layer** — SACK as a native sparse associative memory plugin for existing large language models
- **FPGA implementation** — Tang 9K / Spartan 6 hardware validation pre-ASIC

---

## Licence

SOSL V3 https://ai-lab.host/flatpress/Static-Open-Source-License-v3-0.php

A Recipient-Only Grant is explicitly extended to Anthropic under SOSL v3.0 terms.

---

## Collaboration Note

This repository represents a genuine human-AI collaborative research effort. The architecture, theoretical framework, and creative direction are Nicholas's. The implementation partnership, code architecture, and iterative technical refinement involved Claude (Anthropic) as primary collaborator across multiple extended sessions.

Neither party could have produced this alone in this form. That is the honest account.

---

*"Intelligence is geometry. The field is the memory. The chain is the thought."*

**Nicholas (Nixsy) & Claude — 2026**
