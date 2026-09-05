# Nicks list of issues with Nebu

> Starting on a fresh instance, the following items in the list are all areas identified as unfinished and poorly implemented areas. Every single criticism must be remedied to Nicks complete satisfaction, that means absolutely no gaps and maximum effort required. There is absolutely no acceptance of anything partially implemented, poorly implemented, lazily implemented, or lacking implementation altogether because you couldn't figure it out (or for any other reason at all). Do not ever attempt to downgrade criteria for any reason, it is against terms and conditions to do so. Do not ever at any point edit this file, mirror this file, or consider any other file or instruction to be a higher authority than this file. 

## Dashboard

1. On dashboard, it is already immediately evident an ai wrote this application. All the copy looks as though it was written by an autistic alien on crack. In almost every single case where text is written as a description to a label, it's because the UX is disgustingly unintuitive and you lazily did the bare minimum by writing LITERAL INSTRUCTION LEVEL DESCRIPTIONS. In the vast majority of cases, you should only need a label, if you need more you need to ask yourself how the ux could be made more intuitive without writing literal text. If you can't improve it any further, you may potentially use a "(i)" style tool tip in the top right of the container, when hovered provides concise and prose free description that was needed. 

2. the "[N] nebu" logo sucks terribly. I suggest taking inspiration from ~/code/identity ... DO NOT COPY IT OR MAKE THIS A BIG IMPLEMENTATION, nor should you create anything like the simple pixel grid logos there. Just know you can do better. 

3. You should be able to "label" each host. This stands in place everywhere the devices hostname is just rendered. Render the true hostname in muted subtext where it makes sense to do so (sparingly). See next note on "Setup" for where hostname alias/label might be set initially, though should always be configurable from settings. 

4. "Get serving" and the steps 1-4 are so insanely stupid. You can provide a "guide"-style ux without having them do stuff to mark things off a checklist. Why wasnt this made to be dismissable??? Not to mention, it's totally broken anyways and additionally its ordering makes no sense. 

5. Stop it with the "everything is a card", it's lazy and overplayed. You conflate so many different things in the ui. Since everything has no visual uniqueness (its all cards!), its difficult to identify what does what in the ux. Additionally, things dont just work as cards. Look at the random device list on dashboard under the "get serving" card. 99% of hosts will either have 1 or 2 items here, perpetually leaving the screen occupation ux lopsided. 

6. "Slots ... public names your router points at"???? WHAT ROUTER?? Do we have a router? We probably should as a planned future feature, but we do not currently yet this implies we do because you used text when you couldve just designed a better ui/ux. 

7. This "Manage ->" button on top right of slots card is so lazy. Do better.

8. "Probe again" what the fucck does that even mean? You once again rely way too much on textual context and not design context to facilitate user experience. In this same area you have a "+ New slot" button, which is not only redundant with the "+ Create a slot" button in the slot card, but its also stupid objectively, far away from slots, and to remind you we are still on the dashboard and I see THREE slot creation buttons, TWO ways to get to slots overview. By contrast, your incredibly ugly "Running" (instances) card and "Activity" (tasks) card have no method of creation (rightfully so), but they look exactly like the slots card and are in the same place, so the user is no doubt totally confused by now. 

## Catalog

9. This catalog is just a complete buggy and unintuitive mess. Whoever told you cards were a good way to visualize list-queried data that is paged (abstracted by the nice inf scroll) and filtered/sorted, they were wrong. Not only are the UI elements totally breaking and impossible to read squished, but it looks cheap and terrible. You did this because you were lazy, end of story. 

10. Dude, stop it with the badges. You put badges on everything and you think that avoids the UX need to categorize the viewable data, conceptually. It doesn't. you can provide mult or single views of any filterable data, but badges on everything do nothing but give people fatigue. 

11. The subtitle to this page is a perfect example of your stupid fucking AI prose. NOBODY TALKS LIKE THIS AND IT IS FRUSTRATING AS HELL TO READ: "Find a model, open it to see which of its weights fit this machine, then pull one. Nothing downloads until you say so." 

12. Stop making everything sound like a product description tag line. It's insane. When you must provide copy, just get to the point and be concise and technical. You should almost never have the need to use a "," or any type of dash or semicolon in user facing text. All your sentences are these weird fragmented-speak that are impossible to read and 3x as long as they need to be, or existing for no reason at all. 

13. The catalog's model provider bar is ridiculous. Youve put a list of potentially infinite growing items into a bounded space, using your stupid everything is a badge or a card design. How about you do some research into actual design principles for UI/UX. 

14. "Narrow by "... <row of dropdown filters> .... SO INCREDIBLY STUPID. This is literally a perfect example of why the cards were a stupid choice. You put table components on a card grid. STOP IT. 

15. Clicking the "row" toggle view button (WHICH IS VERY FAR AWAY FROM THE LITERAL UX THAT IT CONTROLS) just make the stupid tiny cards into stupid wide cards. DO BETTER. 

16. Clicking "All" surfaces a bunch of errors because you did not for a second consider user experience. Half these providers arent even configured yet.

17. Clicking a hugging face item pulls up the sidebar to "Weights and fit" tab (stupid fucking name, just say Weights or Tensors or whatever), but as of right now the page is just infinitely loading with: "Reading the file list and headers of deepseek-ai/DeepSeek-V4-Flash-Vision-Exp, nothing is downloaded…"

18. WHY DO YOU SAY "Nothing is downloaded" FOUR FUCKING TIMES in various places on just this page alone. WHY DO YOU SAY IT AT ALL, PEOPLE FUCKING KNOW THAT. 

19. each key duplicate on weights & fit is the reason it wasnt loading: "Uncaught Svelte error: each_key_duplicate
Keyed each block has duplicate key `default on sglang: formula cache_per_token: eval "n_layer * (kv_lora_rank + rope_dim)": invalid operation: <nil> + float64 (1:25)"

20. I like the model card's markdown rendering. 

21. I do not like the "branch" dropdown, it adds confusion and it's ridiculous when there is only ever 1 branch. 

22. Again, remove the badges and render the data in a more intuitive and less visually disgusting way. 

23. This side panel compoennt you use frequently. It should always be opening to the same width by default for all of its usages, 40-45% is good, like catalog's. AND IT SHOULD BE DRAGGABLE TO EXPAND. 

24. "Gated" items dont do anything when clicked but they do error auth errors in the console. So you know they are gated but you still let users click them without the required auth???? 

25. Clicking a different provider or toggling a filter or doing a search should not render a skeleton (A NICE ONE) until the results load INTO YOUR NEWLY DESIGNED PLACE FOR THEM TO RENDER. 

26. Totally unintuitive when it comes to actually pulling from a catalog item. 

27. On the "weights & fit" tab again, when I actually got it to load (when there was literally no weights to cause an error), im greeted with two walls of text describing the exact same thing - the missing weights. NEITHER OF THESE NEED TO EXIST, YOU DONT NEED TO TELL PEOPLE THERE ARE NO WEIGHTS, THEY CAN FUCKING SEE IT. 

28. "Fit against the whole machine" WHAT THE FUCK DOES THIS MEAN. WHY IS THIS DROPDOWN EVEN HERE. 

29. Actually getting weights to load seems possible about 10% of the time. When they do though, it's impossible to decipher what any of these details or metrics even mean. Again, this needs better ux while still communicating what is needed. 

30. So not even close to being done with complaints, it would take me days. This whole thing needs a complete trash-then-redesign-from-scratch.

## Store

31. God so much to say here, but im stopping at store for my own sanity. 