note: seeing case where zero links are extract because the terminology of the chunk is of this form:

[2026.08.13 21:24:19] ⚠️ 0-Link Yield Incident for sess_bf535ed1826b (chunk 1/1):
   ├─ Extracted Nodes: [oksana_kovalenko (human) samir_qureshi (human) radar_glitching (state) wind_picking_up_sw (event) left_hand_tingling (state) not_diving (state) second_guessing_tide_charts (state) onshore (state) watching_horizon (event)]
   └─ Transcript Snippet: "oksana_kovalenko: Hey Samir. How's the weather watching going?\nsamir_qureshi: Wind's picking up from the SW, as usual. Radar's glitching again though ..."

All of the identified entities are of type: human, state, event
This happens because the implicit time-component is not taken into account: the person has made statements of fact in a certain conversation.
One way to solve this is to perfect the extraction prompt to have a temporal "now" indicating the index of the  conversation in the sequence of all conversations, and record links as follow:

oksana_kovalenko (link) asking -> samir_qureshi (qualifier/caveat) How's the weather watching going? (temporal marker of when the conversation occured)
samir_qureshi (link) answering -> oksana_kovalenko (qualifier/caveat) Wind's picking up from the SW, as usual (temporal marker)
samir_qureshi (link) answering -> oksana_kovalenko (qualifier/caveat) Radar's glitching again (temporal marker)

