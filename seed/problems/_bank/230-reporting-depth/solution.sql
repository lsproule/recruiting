with recursive chain as (
  select id, name, 0 as depth from employee where manager_id is null
  union all
  select e.id, e.name, c.depth + 1 from employee e join chain c on e.manager_id = c.id
)
select name, depth from chain